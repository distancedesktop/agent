package main

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/okdaichi/webtransport-go"
)

func handleSession(wtSess *webtransport.Session, cm *certManager) {
	ctx := wtSess.Context()

	sub := &subscriber{sess: wtSess}

	remote := wtSess.RemoteAddr()
	log.Printf("session start from %s", remote)

	stream, err := wtSess.AcceptStream(ctx)
	if err != nil {
		log.Printf("session %s: accept stream: %v", remote, err)
		return
	}
	sub.ctrl = stream
	log.Printf("session %s: control stream accepted", remote)

	stateMu.Lock()
	if state != nil {
		state.subMu.Lock()
		state.subscribers[sub] = struct{}{}
		log.Printf("session %s: subscribed to active stream", remote)
		state.subMu.Unlock()
	}
	stateMu.Unlock()

	defer func() {
		stream.Close()
		stateMu.Lock()
		if state != nil {
			state.subMu.Lock()
			delete(state.subscribers, sub)
			isOwner := state.owner == sub
			state.subMu.Unlock()
			if isOwner {
				log.Printf("session %s: owner disconnected, tearing down stream", remote)
				teardown()
			}
		}
		stateMu.Unlock()
		log.Printf("session %s: closed", remote)
	}()

	if cm != nil {
		log.Printf("session %s: sending fingerprint", remote)
		sendControlMsg(sub, map[string]any{
			"type":        "fingerprint-refresh",
			"algorithm":   "sha-256",
			"fingerprint": cm.FingerprintHex(),
		})
		log.Printf("session %s: fingerprint sent", remote)
	}

	if displays, err := listDisplays(); err != nil {
		log.Printf("session %s: list-displays error: %v", remote, err)
	} else {
		log.Printf("session %s: pushing %d display(s)", remote, len(displays))
		sendControlMsg(sub, map[string]any{"type": "displays", "displays": displays})
	}

	log.Printf("session %s: entering decode loop", remote)
	dec := json.NewDecoder(sub.ctrl)
	for {
		var req struct {
			Type      string `json:"type"`
			DisplayID uint32 `json:"display_id,omitempty"`
			FPS       int    `json:"fps,omitempty"`
			Codec     string `json:"codec,omitempty"`
			Bitrate   int    `json:"bitrate,omitempty"`
		}

		type msgResult struct {
			req interface{}
			err error
		}
		msgCh := make(chan msgResult, 1)
		go func() {
			var m struct {
				Type      string `json:"type"`
				DisplayID uint32 `json:"display_id,omitempty"`
				FPS       int    `json:"fps,omitempty"`
				Codec     string `json:"codec,omitempty"`
				Bitrate   int    `json:"bitrate,omitempty"`
			}
			msgCh <- msgResult{req: &m, err: dec.Decode(&m)}
		}()

		select {
		case res := <-msgCh:
			if res.err != nil {
				log.Printf("session %s: decode: %v", remote, res.err)
				return
			}
			req = *(res.req.(*struct {
				Type      string `json:"type"`
				DisplayID uint32 `json:"display_id,omitempty"`
				FPS       int    `json:"fps,omitempty"`
				Codec     string `json:"codec,omitempty"`
				Bitrate   int    `json:"bitrate,omitempty"`
			}))
		case <-ctx.Done():
			log.Printf("session %s: context cancelled while waiting for message: %v", remote, ctx.Err())
			return
		}

		log.Printf("session %s: received %s (display=%d fps=%d codec=%s bitrate=%d)", remote, req.Type, req.DisplayID, req.FPS, req.Codec, req.Bitrate)

		switch req.Type {
		case "list-displays":
			displays, err := listDisplays()
			if err != nil {
				log.Printf("session %s: list-displays error: %v", remote, err)
				sendControlMsg(sub, map[string]string{"type": "error", "message": err.Error()})
				continue
			}
			log.Printf("session %s: %d displays available", remote, len(displays))
			sendControlMsg(sub, map[string]any{"type": "displays", "displays": displays})

		case "start":
			if req.FPS == 0 {
				req.FPS = 60
			}
			if req.Codec == "" {
				req.Codec = "h264"
			}
			log.Printf("session %s: starting stream display=%d fps=%d codec=%s", remote, req.DisplayID, req.FPS, req.Codec)
			if err := startStream(int(req.DisplayID), req.FPS, req.Codec, req.Bitrate, sub); err != nil {
				log.Printf("session %s: start error: %v", remote, err)
				sendControlMsg(sub, map[string]string{"type": "error", "message": err.Error()})
				continue
			}
			stateMu.Lock()
			w, h := state.width, state.height
			stateMu.Unlock()
			log.Printf("session %s: stream started %dx%d", remote, w, h)
			sendControlMsg(sub, map[string]any{
				"type":   "started",
				"width":  w,
				"height": h,
				"codec":  req.Codec,
			})

		case "stop":
			log.Printf("session %s: stopping stream", remote)
			stateMu.Lock()
			if state != nil {
				teardown()
			}
			stateMu.Unlock()
			sendControlMsg(sub, map[string]string{"type": "stopped"})

		default:
			log.Printf("session %s: unknown message type: %s", remote, req.Type)
			sendControlMsg(sub, map[string]string{"type": "error", "message": fmt.Sprintf("unknown type: %s", req.Type)})
		}
	}
}

func certRotationLoop(cm *certManager) {
	for {
		time.Sleep(1 * time.Hour)
		if !cm.NeedsRotation() {
			continue
		}
		fp, err := cm.Rotate()
		if err != nil {
			log.Printf("cert rotation: %v", err)
			continue
		}
		fpHex := fmt.Sprintf("%x", fp)
		log.Printf("cert rotated, new fingerprint: %s", fpHex)
		broadcastControlMsg(map[string]any{
			"type":        "fingerprint-refresh",
			"algorithm":   "sha-256",
			"fingerprint": fpHex,
		})
	}
}

func broadcastControlMsg(msg map[string]any) {
	stateMu.Lock()
	defer stateMu.Unlock()
	if state == nil {
		return
	}
	state.subMu.Lock()
	defer state.subMu.Unlock()
	log.Printf("broadcast: %s to %d subscriber(s)", msg["type"], len(state.subscribers))
	for sub := range state.subscribers {
		sub.ctrlMu.Lock()
		json.NewEncoder(sub.ctrl).Encode(msg)
		sub.ctrlMu.Unlock()
	}
}

func sendControlMsg(sub *subscriber, msg any) {
	if m, ok := msg.(map[string]any); ok {
		log.Printf("send to %s: %s", sub.sess.RemoteAddr(), m["type"])
	} else if m, ok := msg.(map[string]string); ok {
		log.Printf("send to %s: %s", sub.sess.RemoteAddr(), m["type"])
	}
	sub.ctrlMu.Lock()
	json.NewEncoder(sub.ctrl).Encode(msg)
	sub.ctrlMu.Unlock()
}
