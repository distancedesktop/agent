package backend

// Backend "captured": spawns distancedesktop/captured as a child process.
// captured captures and encodes in one process. Control is JSON-RPC 2.0 on
// its stdin/stdout, and encoded access units come back as records on fd 3
// (see captured's README for the record layout).

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"time"
)

func init() { Register(&CapturedBackend{}) }

// CapturedBackend runs captured as a child per stream.
type CapturedBackend struct {
	// Bin is the captured binary. Empty uses $CAPTURED_BIN, then "captured"
	// from PATH.
	Bin string
	// Props are extra properties for start, e.g. "pipeline" or
	// "video.encoder". Values are JSON.
	Props map[string]json.RawMessage
}

func (b *CapturedBackend) Name() string { return "captured" }

func (b *CapturedBackend) bin() string {
	if b.Bin != "" {
		return b.Bin
	}
	if v := os.Getenv("CAPTURED_BIN"); v != "" {
		return v
	}
	return "captured"
}

// child is one running captured process.
type child struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	media   *os.File
	mu      sync.Mutex
	id      int
	wait    map[int]chan rpcReply
	closed  bool
	stopped chan string // stopped notifications
}

type rpcReply struct {
	Result json.RawMessage
	Err    error
}

func (b *CapturedBackend) spawn(withMedia bool) (*child, error) {
	cmd := exec.Command(b.bin())
	cmd.Stderr = os.Stderr
	c := &child{cmd: cmd, wait: map[int]chan rpcReply{}, stopped: make(chan string, 1)}
	var mw *os.File
	if withMedia {
		mr, w, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		c.media, mw = mr, w
		passMedia(cmd, mw)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c.stdin = stdin
	if err := cmd.Start(); err != nil {
		if c.media != nil {
			c.media.Close()
			mw.Close()
		}
		return nil, fmt.Errorf("captured: %w", err)
	}
	if mw != nil {
		mw.Close()
	}
	go c.read(stdout)
	return c, nil
}

func (c *child) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var m struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.ID == nil {
			if m.Method == "stopped" {
				var p struct {
					Error string `json:"error"`
				}
				json.Unmarshal(m.Params, &p)
				select {
				case c.stopped <- p.Error:
				default:
				}
			}
			continue
		}
		c.mu.Lock()
		ch := c.wait[*m.ID]
		delete(c.wait, *m.ID)
		c.mu.Unlock()
		if ch == nil {
			continue
		}
		rep := rpcReply{Result: m.Result}
		if m.Error != nil {
			rep.Err = errors.New(m.Error.Message)
		}
		ch <- rep
	}
	c.mu.Lock()
	c.closed = true
	for id, ch := range c.wait {
		ch <- rpcReply{Err: errors.New("captured exited")}
		delete(c.wait, id)
	}
	c.mu.Unlock()
}

func (c *child) call(ctx context.Context, method string, params, out any) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("captured exited")
	}
	c.id++
	id := c.id
	ch := make(chan rpcReply, 1)
	c.wait[id] = ch
	c.mu.Unlock()
	msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if _, err := c.stdin.Write(append(msg, '\n')); err != nil {
		return err
	}
	select {
	case rep := <-ch:
		if rep.Err != nil {
			return fmt.Errorf("captured %s: %w", method, rep.Err)
		}
		if out != nil {
			return json.Unmarshal(rep.Result, out)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// close ends the child: closing stdin makes captured exit; kill if it doesn't.
func (c *child) close() {
	c.stdin.Close()
	done := make(chan struct{})
	go func() { c.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		c.cmd.Process.Kill()
		<-done
	}
	if c.media != nil {
		c.media.Close()
	}
}

// ListDisplays returns the displays of the first pipeline that works here,
// which is the one captured picks with pipeline=auto.
func (b *CapturedBackend) ListDisplays(ctx context.Context) ([]Display, error) {
	c, err := b.spawn(false)
	if err != nil {
		return nil, err
	}
	defer c.close()
	var res struct {
		Pipelines []struct {
			Name     string    `json:"name"`
			Error    string    `json:"error"`
			Displays []Display `json:"displays"`
		} `json:"pipelines"`
	}
	if err := c.call(ctx, "list", nil, &res); err != nil {
		return nil, err
	}
	var errs []error
	for _, p := range res.Pipelines {
		if p.Error != "" {
			errs = append(errs, fmt.Errorf("%s: %s", p.Name, p.Error))
			continue
		}
		log.Printf("captured: pipeline %s, %d display(s)", p.Name, len(p.Displays))
		return p.Displays, nil
	}
	return nil, fmt.Errorf("captured: no pipeline works here: %w", errors.Join(errs...))
}

// capturedStream is one captured child streaming encoded video.
type capturedStream struct {
	c      *child
	req    StartRequest
	width  int
	height int
	codec  string
	chunks chan H264Chunk
	once   sync.Once
}

func (s *capturedStream) Chunks() <-chan H264Chunk { return s.chunks }
func (s *capturedStream) Width() int               { return s.width }
func (s *capturedStream) Height() int              { return s.height }
func (s *capturedStream) FPS() int                 { return s.req.FPS }
func (s *capturedStream) Codec() string            { return s.codec }

func (s *capturedStream) Close() error {
	s.once.Do(s.c.close)
	return nil
}

// RequestKeyframe asks for an IDR on the next frame, for late joiners.
func (s *capturedStream) RequestKeyframe() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.c.call(ctx, "set", map[string]any{"video.keyframe": true}, nil); err != nil {
		log.Printf("captured: keyframe: %v", err)
	}
}

// SetBitrate changes the target bitrate while streaming.
func (s *capturedStream) SetBitrate(bps int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.c.call(ctx, "set", map[string]any{"video.bitrate": bps}, nil)
}

// StartStream spawns captured, starts capture+encode and forwards its access
// units.
func (b *CapturedBackend) StartStream(ctx context.Context, req StartRequest) (Stream, error) {
	if req.FPS <= 0 {
		req.FPS = 60
	}
	if req.Codec == "" {
		req.Codec = "h264"
	}
	c, err := b.spawn(true)
	if err != nil {
		return nil, err
	}
	params := map[string]any{
		"display":     req.DisplayID,
		"video.fps":   req.FPS,
		"video.codec": req.Codec,
	}
	if req.Bitrate > 0 {
		params["video.bitrate"] = req.Bitrate
	}
	for k, v := range b.Props {
		params[k] = v
	}
	var res struct {
		Pipeline string `json:"video.active_pipeline"`
		Encoder  string `json:"video.active_encoder"`
		HW       string `json:"video.hw"`
		Size     [2]int `json:"video.size"`
	}
	if err := c.call(ctx, "start", params, &res); err != nil {
		c.close()
		return nil, err
	}
	log.Printf("captured: %s %dx%d@%d with %s (hw %q)", res.Pipeline, res.Size[0], res.Size[1], req.FPS, res.Encoder, res.HW)
	s := &capturedStream{
		c:      c,
		req:    req,
		width:  res.Size[0],
		height: res.Size[1],
		codec:  req.Codec,
		chunks: make(chan H264Chunk, 128),
	}
	go func() {
		if msg := <-c.stopped; msg != "" {
			log.Printf("captured: stream stopped: %s", msg)
			s.Close()
		}
	}()
	go s.readMedia()
	return s, nil
}

const (
	recVideo  = 0
	recFormat = 2
	flagKey   = 1
	maxRecord = 64 << 20
)

// readMedia turns media records into chunks until captured goes away.
func (s *capturedStream) readMedia() {
	defer close(s.chunks)
	defer s.Close()
	r := bufio.NewReaderSize(s.c.media, 1<<20)
	var hdr [16]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(hdr[0:])
		if n > maxRecord {
			log.Printf("captured: record of %d bytes, giving up", n)
			return
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return
		}
		switch hdr[4] {
		case recFormat:
			log.Printf("captured: format %s", buf)
		case recVideo:
			s.chunks <- H264Chunk{Data: buf, Keyframe: hdr[5]&flagKey != 0, AccessUnit: true}
		}
	}
}
