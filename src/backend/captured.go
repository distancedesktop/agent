package backend

// Backend "captured": talks to the distancedesktop/captured unix-socket
// daemon. Control channel speaks JSON requests/responses; the daemon hands
// back a media socket carrying raw BGRA frames (8-byte big-endian
// width/height header on the first frame, then per-frame headers), which we
// pipe through ffmpeg to produce H.264 Annex B.
//
// The wire protocol is unchanged — existing captured daemons keep working.

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

func init() { Register(&CapturedBackend{}) }

// CapturedBackend captures via the captured daemon + ffmpeg encode.
type CapturedBackend struct{}

func (b *CapturedBackend) Name() string { return "captured" }

// SocketPath returns the captured control socket path.
func (b *CapturedBackend) SocketPath() string {
	if v := os.Getenv("CAPTURED_SOCKET"); v != "" {
		return v
	}
	return "/tmp/captured.socket"
}

type capturedDisplayResp struct {
	Displays []struct {
		ID          uint32  `json:"id"`
		Width       int     `json:"width"`
		Height      int     `json:"height"`
		X           int     `json:"x"`
		Y           int     `json:"y"`
		RefreshRate float64 `json:"refresh_rate"`
	} `json:"displays"`
	Error string `json:"error,omitempty"`
}

// ListDisplays queries the captured daemon over its unix control socket.
func (b *CapturedBackend) ListDisplays(ctx context.Context) ([]Display, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", b.SocketPath())
	if err != nil {
		return nil, fmt.Errorf("captured: %w", err)
	}
	defer conn.Close()
	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	log.Printf("captured: sending list-displays")
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	if err := enc.Encode(map[string]string{"type": "list-displays"}); err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	var resp capturedDisplayResp
	if err := dec.Decode(&resp); err != nil {
		log.Printf("captured: list-displays decode error: %v", err)
		return nil, err
	}
	if resp.Error != "" {
		log.Printf("captured: list-displays error: %s", resp.Error)
		return nil, errors.New(resp.Error)
	}
	log.Printf("captured: got %d display(s)", len(resp.Displays))
	out := make([]Display, len(resp.Displays))
	for i, d := range resp.Displays {
		log.Printf("captured: display[%d] id=%d %dx%d @ (x=%d,y=%d) %.2fhz", i, d.ID, d.Width, d.Height, d.X, d.Y, d.RefreshRate)
		out[i] = Display{ID: d.ID, Width: d.Width, Height: d.Height, X: d.X, Y: d.Y, RefreshRate: d.RefreshRate}
	}
	return out, nil
}

// capturedStream implements Stream for the captured+ffmpeg pipeline.
type capturedStream struct {
	req     StartRequest
	width   int
	height  int
	ctrl    net.Conn
	media   net.Conn
	ffmpeg  *exec.Cmd
	stdin   io.WriteCloser
	chunks  chan H264Chunk
	cancel  context.CancelFunc
	closeMu sync.Mutex
	closed  bool
}

func (s *capturedStream) Chunks() <-chan H264Chunk { return s.chunks }
func (s *capturedStream) Width() int               { return s.width }
func (s *capturedStream) Height() int              { return s.height }
func (s *capturedStream) FPS() int                 { return s.req.FPS }
func (s *capturedStream) Codec() string            { return s.req.Codec }

func (s *capturedStream) Close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.cancel()
	if s.ffmpeg != nil && s.ffmpeg.Process != nil {
		_ = s.ffmpeg.Process.Kill()
	}
	if s.stdin != nil {
		s.stdin.Close()
	}
	if s.ffmpeg != nil {
		done := make(chan struct{})
		go func() { s.ffmpeg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
	s.media.Close()
	json.NewEncoder(s.ctrl).Encode(map[string]string{"type": "stop-stream"})
	s.ctrl.Close()
	return nil
}

// StartStream dials start-stream on the control socket, opens the media
// socket, spawns ffmpeg and returns a chunk stream.
func (b *CapturedBackend) StartStream(ctx context.Context, req StartRequest) (Stream, error) {
	if req.FPS <= 0 {
		req.FPS = 60
	}
	var d net.Dialer
	ctrl, err := d.DialContext(ctx, "unix", b.SocketPath())
	if err != nil {
		return nil, fmt.Errorf("captured control: %w", err)
	}
	enc := json.NewEncoder(ctrl)
	dec := json.NewDecoder(ctrl)

	log.Printf("captured: sending start-stream display=%d fps=%d", req.DisplayID, req.FPS)
	if err := enc.Encode(map[string]any{
		"type":       "start-stream",
		"display_id": req.DisplayID,
		"fps":        req.FPS,
	}); err != nil {
		ctrl.Close()
		return nil, fmt.Errorf("start-stream: %w", err)
	}
	var streamResp struct {
		Type   string `json:"type"`
		Socket string `json:"socket"`
		Format string `json:"format"`
		Error  string `json:"error,omitempty"`
	}
	if err := dec.Decode(&streamResp); err != nil {
		ctrl.Close()
		return nil, fmt.Errorf("start-stream response: %w", err)
	}
	if streamResp.Error != "" {
		log.Printf("captured: start-stream error: %s", streamResp.Error)
		ctrl.Close()
		return nil, errors.New(streamResp.Error)
	}
	log.Printf("captured: start-stream ok socket=%s format=%s", streamResp.Socket, streamResp.Format)

	media, err := d.DialContext(ctx, "unix", streamResp.Socket)
	if err != nil {
		ctrl.Close()
		return nil, fmt.Errorf("media socket: %w", err)
	}

	if deadline, ok := ctx.Deadline(); ok {
		media.SetReadDeadline(deadline)
	}

	var hdr [8]byte
	if _, err := io.ReadFull(media, hdr[:]); err != nil {
		ctrl.Close()
		media.Close()
		return nil, fmt.Errorf("first frame header: %w", err)
	}
	w := int(binary.BigEndian.Uint32(hdr[0:4]))
	h := int(binary.BigEndian.Uint32(hdr[4:8]))
	const maxDim = 16384
	if w <= 0 || h <= 0 || w > maxDim || h > maxDim {
		ctrl.Close()
		media.Close()
		return nil, fmt.Errorf("first frame: invalid dimensions %dx%d", w, h)
	}
	const maxPixels = maxDim * maxDim
	if int64(w)*int64(h) > maxPixels {
		ctrl.Close()
		media.Close()
		return nil, fmt.Errorf("first frame: dimension overflow %dx%d", w, h)
	}
	log.Printf("captured: first frame %dx%d", w, h)
	// handshake done — clear deadline for steady-state streaming reads
	media.SetReadDeadline(time.Time{})
	firstFrame := make([]byte, w*h*4)
	if _, err := io.ReadFull(media, firstFrame); err != nil {
		ctrl.Close()
		media.Close()
		return nil, fmt.Errorf("first frame data: %w", err)
	}

	encoder := probeEncoder()
	args := buildFFmpegArgs(encoder, req, w, h)

	cmd := exec.Command("ffmpeg", args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		ctrl.Close()
		media.Close()
		return nil, fmt.Errorf("ffmpeg stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		ctrl.Close()
		media.Close()
		return nil, fmt.Errorf("ffmpeg stdout: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		stdin.Close()
		ctrl.Close()
		media.Close()
		return nil, fmt.Errorf("ffmpeg start: %w", err)
	}
	if _, err := stdin.Write(firstFrame); err != nil {
		stdin.Close()
		cmd.Wait()
		ctrl.Close()
		media.Close()
		return nil, fmt.Errorf("write first frame: %w", err)
	}
	log.Printf("encoder: %s  %dx%d @ %dfps", encoder, w, h, req.FPS)

	ctx2, cancel := context.WithCancel(context.Background())
	s := &capturedStream{
		req:    req,
		width:  w,
		height: h,
		ctrl:   ctrl,
		media:  media,
		ffmpeg: cmd,
		stdin:  stdin,
		chunks: make(chan H264Chunk, 128),
		cancel: cancel,
	}

	// BGRA frames -> ffmpeg stdin.
	go func() {
		var buf [8]byte
		const maxDim = 16384
		const maxPixels = maxDim * maxDim
		for {
			if _, err := io.ReadFull(media, buf[:]); err != nil {
				break
			}
			fw := int(binary.BigEndian.Uint32(buf[0:4]))
			fh := int(binary.BigEndian.Uint32(buf[4:8]))
			if fw <= 0 || fh <= 0 || fw > maxDim || fh > maxDim || int64(fw)*int64(fh) > maxPixels {
				log.Printf("captured: skipping invalid frame dimensions %dx%d", fw, fh)
				break
			}
			frame := make([]byte, fw*fh*4)
			if _, err := io.ReadFull(media, frame); err != nil {
				break
			}
			if ctx2.Err() != nil {
				break
			}
			if _, err := stdin.Write(frame); err != nil {
				break
			}
		}
		stdin.Close()
	}()

	// ffmpeg stdout -> chunks channel (Annex B already chunked by muxer).
	go func() {
		defer close(s.chunks)
		r := bufio.NewReaderSize(stdout, 1<<16)
		buf := make([]byte, 65536)
		for ctx2.Err() == nil {
			n, err := r.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				select {
				case s.chunks <- H264Chunk{Data: chunk}:
				case <-ctx2.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	return s, nil
}

func buildFFmpegArgs(encoder string, req StartRequest, w, h int) []string {
	args := []string{
		"-y",
		"-f", "rawvideo",
		"-pix_fmt", "bgra",
		"-s", fmt.Sprintf("%dx%d", w, h),
		"-r", strconv.Itoa(req.FPS),
		"-i", "pipe:0",
		"-c:v", encoder,
		"-pix_fmt", "yuv420p",
	}
	switch encoder {
	case "h264_videotoolbox", "hevc_videotoolbox":
		args = append(args, "-realtime", "true")
	case "h264_nvenc":
		args = append(args, "-preset", "p1", "-tune", "ull")
	case "h264_amf":
		args = append(args, "-usage", "ultralowlatency", "-quality", "speed")
	case "h264_vaapi":
		args = append(args, "-compression_level", "1")
	case "h264_qsv":
		args = append(args, "-preset", "veryfast")
	}
	switch req.Codec {
	case "hevc":
		args = append(args, "-f", "hevc")
	case "av1":
		args = append(args, "-f", "av1")
	case "vp9":
		args = append(args, "-f", "ivf")
	default:
		args = append(args, "-f", "h264")
	}
	if req.Bitrate > 0 {
		args = append(args, "-b:v", strconv.Itoa(req.Bitrate))
	}
	args = append(args, "-")
	return args
}

func probeEncoder() string {
	out, err := exec.Command("ffmpeg", "-encoders").Output()
	if err != nil {
		return "libx264"
	}
	s := string(out)
	prefs := []string{"h264_videotoolbox", "hevc_videotoolbox", "h264_nvenc", "h264_amf", "h264_qsv", "h264_vaapi"}
	for _, name := range prefs {
		if strings.Contains(s, name) {
			return name
		}
	}
	return "libx264"
}
