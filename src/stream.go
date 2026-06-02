package main

import (
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
	"time"

	"github.com/qumo-dev/gomoqt/moqt"
)

func startStream(displayID, fps int, codec string, bitrate int, caller *subscriber) error {
	stateMu.Lock()
	defer stateMu.Unlock()

	if state != nil {
		log.Printf("startStream: late-join, attaching to existing stream")
		videoStream, err := caller.sess.OpenUniStream()
		if err != nil {
			return fmt.Errorf("video stream: %w", err)
		}
		caller.video = videoStream
		state.subMu.Lock()
		state.subscribers[caller] = struct{}{}
		state.subMu.Unlock()
		log.Printf("startStream: late-join complete, now %d subscriber(s)", len(state.subscribers))
		return nil
	}

	log.Printf("startStream: dialing captured socket %s", capturedSocket())
	ctrl, err := net.Dial("unix", capturedSocket())
	if err != nil {
		return fmt.Errorf("captured control: %w", err)
	}

	enc := json.NewEncoder(ctrl)
	dec := json.NewDecoder(ctrl)

	log.Printf("captured: sending start-stream display=%d fps=%d", displayID, fps)
	if err := enc.Encode(map[string]any{
		"type":       "start-stream",
		"display_id": uint32(displayID),
		"fps":        fps,
	}); err != nil {
		ctrl.Close()
		return fmt.Errorf("start-stream: %w", err)
	}
	var streamResp struct {
		Type   string `json:"type"`
		Socket string `json:"socket"`
		Format string `json:"format"`
		Error  string `json:"error,omitempty"`
	}
	if err := dec.Decode(&streamResp); err != nil {
		ctrl.Close()
		return fmt.Errorf("start-stream response: %w", err)
	}
	if streamResp.Error != "" {
		log.Printf("captured: start-stream error: %s", streamResp.Error)
		ctrl.Close()
		return errors.New(streamResp.Error)
	}
	log.Printf("captured: start-stream ok socket=%s format=%s", streamResp.Socket, streamResp.Format)

	media, err := net.Dial("unix", streamResp.Socket)
	if err != nil {
		ctrl.Close()
		return fmt.Errorf("media socket: %w", err)
	}

	var hdr [8]byte
	if _, err := io.ReadFull(media, hdr[:]); err != nil {
		ctrl.Close()
		media.Close()
		return fmt.Errorf("first frame header: %w", err)
	}
	w := int(binary.BigEndian.Uint32(hdr[0:4]))
	h := int(binary.BigEndian.Uint32(hdr[4:8]))
	log.Printf("captured: first frame %dx%d", w, h)
	firstFrame := make([]byte, w*h*4)
	if _, err := io.ReadFull(media, firstFrame); err != nil {
		ctrl.Close()
		media.Close()
		return fmt.Errorf("first frame data: %w", err)
	}

	encoder := probeEncoder()

	args := []string{
		"-y",
		"-f", "rawvideo",
		"-pix_fmt", "bgra",
		"-s", fmt.Sprintf("%dx%d", w, h),
		"-r", strconv.Itoa(fps),
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

	switch codec {
	case "hevc":
		args = append(args, "-f", "hevc")
	case "av1":
		args = append(args, "-f", "av1")
	case "vp9":
		args = append(args, "-f", "ivf")
	default:
		args = append(args, "-f", "h264")
	}

	if bitrate > 0 {
		args = append(args, "-b:v", strconv.Itoa(bitrate))
	}

	args = append(args, "-")

	cmd := exec.Command("ffmpeg", args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		ctrl.Close()
		media.Close()
		return fmt.Errorf("ffmpeg stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		ctrl.Close()
		media.Close()
		return fmt.Errorf("ffmpeg stdout: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		stdin.Close()
		ctrl.Close()
		media.Close()
		return fmt.Errorf("ffmpeg start: %w", err)
	}

	stdin.Write(firstFrame)

	log.Printf("encoder: %s  %dx%d @ %dfps", encoder, w, h, fps)

	videoStream, err := caller.sess.OpenUniStream()
	if err != nil {
		stdin.Close()
		cmd.Wait()
		ctrl.Close()
		media.Close()
		return fmt.Errorf("video stream: %w", err)
	}
	caller.video = videoStream

	pubCtx, pubCancel := context.WithCancel(context.Background())

	state = &streamState{
		displayID:     displayID,
		width:         w,
		height:        h,
		fps:           fps,
		capturedCtrl:  ctrl,
		capturedMedia: media,
		ffmpeg:        cmd,
		ffmpegIn:      stdin,
		ffmpegOut:     stdout,
		subscribers:   make(map[*subscriber]struct{}),
		moqTracks:     make(map[*moqt.TrackWriter]struct{}),
		stopPub:       pubCancel,
		owner:         caller,
	}

	state.subscribers[caller] = struct{}{}

	go func() {
		var buf [8]byte
		for {
			if _, err := io.ReadFull(media, buf[:]); err != nil {
				break
			}
			fw := int(binary.BigEndian.Uint32(buf[0:4]))
			fh := int(binary.BigEndian.Uint32(buf[4:8]))
			frame := make([]byte, fw*fh*4)
			if _, err := io.ReadFull(media, frame); err != nil {
				break
			}
			if _, err := stdin.Write(frame); err != nil {
				break
			}
		}
		stdin.Close()
	}()

	go publishStream(pubCtx, stdout)

	return nil
}

func publishStream(ctx context.Context, r io.ReadCloser) {
	buf := make([]byte, 65536)
	for {
		n, err := r.Read(buf)
		if err != nil {
			return
		}
		if ctx.Err() != nil {
			return
		}

		stateMu.Lock()
		ss := state
		stateMu.Unlock()
		if ss == nil {
			continue
		}

		ss.subMu.Lock()
		if ss.subscribers == nil {
			ss.subMu.Unlock()
			return
		}
		for sub := range ss.subscribers {
			if sub.video == nil {
				continue
			}
			sub.video.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
			if _, err := sub.video.Write(buf[:n]); err != nil {
				sub.video.Close()
				delete(ss.subscribers, sub)
			}
		}
		ss.subMu.Unlock()

		ss.moqTrackMu.Lock()
		for tw := range ss.moqTracks {
			if ctx.Err() != nil {
				ss.moqTrackMu.Unlock()
				return
			}
			g, err := tw.OpenGroup()
			if err != nil {
				delete(ss.moqTracks, tw)
				continue
			}
			f := moqt.NewFrame(n)
			f.Write(buf[:n])
			if err := g.WriteFrame(f); err != nil {
				g.CancelWrite(0)
				delete(ss.moqTracks, tw)
				continue
			}
			g.Close()
		}
		ss.moqTrackMu.Unlock()
	}
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

func teardown() {
	if state == nil {
		return
	}
	log.Printf("teardown: stopping stream (display=%d %dx%d)", state.displayID, state.width, state.height)
	if state.stopPub != nil {
		state.stopPub()
	}
	if state.ffmpegIn != nil {
		state.ffmpegIn.Close()
	}
	if state.ffmpeg != nil {
		state.ffmpeg.Wait()
	}
	if state.capturedMedia != nil {
		state.capturedMedia.Close()
	}
	if state.capturedCtrl != nil {
		log.Printf("captured: sending stop-stream")
		json.NewEncoder(state.capturedCtrl).Encode(map[string]string{"type": "stop-stream"})
		state.capturedCtrl.Close()
	}

	state.subMu.Lock()
	subCount := len(state.subscribers)
	for sub := range state.subscribers {
		sendControlMsg(sub, map[string]string{"type": "stream-ended"})
		if sub.video != nil {
			sub.video.Close()
		}
		sub.sess.CloseWithError(0, "stream ended")
	}
	log.Printf("teardown: closed %d subscriber(s)", subCount)
	state.subscribers = nil
	state.subMu.Unlock()

	state.moqTrackMu.Lock()
	trackCount := len(state.moqTracks)
	for tw := range state.moqTracks {
		tw.Close()
	}
	state.moqTracks = nil
	state.moqTrackMu.Unlock()
	log.Printf("teardown: closed %d moq track(s)", trackCount)

	moqBroadcastCancel()

	state = nil
}
