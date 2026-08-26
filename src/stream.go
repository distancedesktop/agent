package main

import (
	"context"
	"fmt"
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
	stream, err := b.StartStream(context.Background(), req)
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
		owner:       caller,
	}

	state.subscribers[caller] = struct{}{}

	go publishStream(pubCtx, state)

	return nil
}

// publishStream fans backend chunks out to all subscribers.
func publishStream(ctx context.Context, ss *streamState) {
	for chunk := range ss.stream.Chunks() {
		if ctx.Err() != nil {
			return
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
			if _, err := sub.video.Write(chunk.Data); err != nil {
				sub.video.Close()
				delete(ss.subscribers, sub)
			}
		}
		ss.subMu.Unlock()
	}
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
		sub.sess.CloseWithError(0, "stream ended")
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
