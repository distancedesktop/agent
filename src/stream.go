package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"time"

	"distancedesktop/agent/src/backend"
)

// startStream begins a video stream via the active backend and attaches the
// caller. Late-joiners attach to an existing stream.
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

	b := activeBackend
	if b == nil {
		var err error
		b, err = backend.Get("captured")
		if err != nil {
			return err
		}
		activeBackend = b
	}

	req := backend.StartRequest{
		DisplayID: uint32(displayID),
		FPS:       fps,
		Codec:     codec,
		Bitrate:   bitrate,
	}
	startCtx, startCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer startCancel()
	stream, err := b.StartStream(startCtx, req)
	if err != nil {
		return fmt.Errorf("%s start-stream: %w", b.Name(), err)
	}
	log.Printf("backend %s: stream started %dx%d @ %dfps", b.Name(), stream.Width(), stream.Height(), stream.FPS())

	videoStream, err := caller.sess.OpenUniStream()
	if err != nil {
		stream.Close()
		return fmt.Errorf("video stream: %w", err)
	}
	caller.video = videoStream

	pubCtx, pubCancel := context.WithCancel(context.Background())

	state = &streamState{
		stream:      stream,
		subscribers: make(map[*subscriber]struct{}),
		stopPub:     pubCancel,
		streamStart: time.Now(),
		owner:       caller,
	}

	state.subscribers[caller] = struct{}{}

	go publishStream(pubCtx, state)

	return nil
}

// publishStream fans backend chunks out to all subscribers.
func publishStream(ctx context.Context, ss *streamState) {
	defer func() {
		stateMu.Lock()
		if state == ss {
			teardown()
		}
		stateMu.Unlock()
	}()
	fr := &framer{}
	publish := func(au []byte) {
		frame := encodeFrame(boolToByte(keyframe(au)), uint64(time.Since(ss.streamStart).Milliseconds()), au)
		ss.subMu.Lock()
		defer ss.subMu.Unlock()
		if ss.subscribers == nil {
			return
		}
		for sub := range ss.subscribers {
			if sub.video == nil {
				continue
			}
			sub.video.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
			if err := writeFrame(sub.video, frame); err != nil {
				sub.video.Close()
				delete(ss.subscribers, sub)
			}
		}
	}
	for chunk := range ss.stream.Chunks() {
		if ctx.Err() != nil {
			return
		}
		for _, au := range fr.Push(chunk.Data) {
			if ctx.Err() != nil {
				return
			}
			publish(au)
		}
	}
	for _, au := range fr.Flush() {
		if ctx.Err() != nil {
			return
		}
		publish(au)
	}
}

func writeFrame(w interface{ Write([]byte) (int, error) }, frame []byte) error {
	for len(frame) > 0 {
		n, err := w.Write(frame)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrNoProgress
		}
		frame = frame[n:]
	}
	return nil
}

func boolToByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

func teardown() {
	if state == nil {
		return
	}
	ss := state
	log.Printf("teardown: stopping stream")

	ss.subMu.Lock()
	subCount := len(ss.subscribers)
	for sub := range ss.subscribers {
		sendControlMsg(sub, map[string]string{"type": "stream-ended"})
		if sub.video != nil {
			sub.video.Close()
		}
	}
	log.Printf("teardown: closed %d subscriber(s)", subCount)
	ss.subscribers = nil
	ss.subMu.Unlock()

	if ss.stopPub != nil {
		ss.stopPub()
	}
	if ss.stream != nil {
		if err := ss.stream.Close(); err != nil {
			log.Printf("teardown: stream close: %v", err)
		}
	}

	state = nil
}
