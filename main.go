package main

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
	_ "embed"
)

//go:embed web/index.html
var webHTML string

type subscriber struct {
	sess   *webtransport.Session
	ctrl   *webtransport.Stream
	ctrlMu sync.Mutex
	video  *webtransport.SendStream
}

type streamState struct {
	displayID int
	width     int
	height    int
	fps       int

	capturedCtrl  net.Conn
	capturedMedia net.Conn

	ffmpeg    *exec.Cmd
	ffmpegIn  io.WriteCloser
	ffmpegOut io.ReadCloser

	subscribers map[*subscriber]struct{}
	subMu       sync.Mutex
	stopPub     context.CancelFunc

	owner *subscriber
}

var (
	state   *streamState
	stateMu sync.Mutex
)

func main() {
	addr := flag.String("addr", ":52020", "WebTransport listen address (UDP)")
	webAddr := flag.String("web", ":52022", "Web UI listen address (TCP)")
	fingerprintOnly := flag.Bool("fingerprint", false, "print the SHA-256 fingerprint and exit")
	customCert := flag.String("cert", "", "TLS certificate path (ECDSA P-256 PEM)")
	customKey := flag.String("key", "", "TLS private key path (ECDSA P-256 PEM)")
	flag.Parse()

	var tlsConfig *tls.Config
	var cm *certManager

	if *customCert != "" && *customKey != "" {
		cer, err := tls.LoadX509KeyPair(*customCert, *customKey)
		if err != nil {
			log.Fatalf("load cert: %v", err)
		}
		tlsConfig = &tls.Config{
			Certificates: []tls.Certificate{cer},
			NextProtos:   []string{"h3"},
		}
	} else {
		var err error
		cm, err = newCertManager(configDir())
		if err != nil {
			log.Fatalf("cert manager: %v", err)
		}
		if *fingerprintOnly {
			fmt.Println(cm.FingerprintHex())
			return
		}
		tlsConfig = cm.TLSConfig()
	}

	wtServer := &webtransport.Server{
		H3: &http3.Server{
			Addr:      *addr,
			TLSConfig: tlsConfig,
			QUICConfig: &quic.Config{
				EnableDatagrams: true,
			},
		},
	}

	wtMux := http.NewServeMux()
	wtMux.HandleFunc("/wt", func(w http.ResponseWriter, r *http.Request) {
		s, err := wtServer.Upgrade(w, r)
		if err != nil {
			log.Printf("upgrade: %v", err)
			w.WriteHeader(400)
			return
		}
		go handleSession(s, cm)
	})
	wtServer.H3.Handler = wtMux

	udpAddr, err := net.ResolveUDPAddr("udp", *addr)
	if err != nil {
		log.Fatalf("resolve udp: %v", err)
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		log.Fatalf("listen udp %s: %v", *addr, err)
	}

	webtransport.ConfigureHTTP3Server(wtServer.H3)

	go func() {
		log.Printf("WebTransport on udp://%s", *addr)
		if err := wtServer.Serve(udpConn); err != nil {
			log.Fatalf("webtransport: %v", err)
		}
	}()

	if cm != nil {
		go certRotationLoop(cm)
		go startWebUI(*webAddr, cm)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig

	stateMu.Lock()
	if state != nil {
		teardown()
	}
	stateMu.Unlock()
	wtServer.Close()
}

// -- cert rotation --

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
	for sub := range state.subscribers {
		sub.ctrlMu.Lock()
		json.NewEncoder(sub.ctrl).Encode(msg)
		sub.ctrlMu.Unlock()
	}
}

func sendControlMsg(sub *subscriber, msg any) {
	sub.ctrlMu.Lock()
	json.NewEncoder(sub.ctrl).Encode(msg)
	sub.ctrlMu.Unlock()
}

// -- Web UI --

func startWebUI(addr string, cm *certManager) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(webHTML))
	})
	mux.HandleFunc("/api/info", func(w http.ResponseWriter, r *http.Request) {
		ips := localIPs()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"fingerprint": cm.FingerprintHex(),
			"ips":         ips,
		})
	})
	log.Printf("Web UI on http://%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("web ui: %v", err)
	}
}

func localIPs() []string {
	var ips []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok {
				if ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
					continue
				}
				if ip4 := ipnet.IP.To4(); ip4 != nil {
					ips = append(ips, ip4.String())
				}
			}
		}
	}
	return ips
}

// -- WebTransport session handling --

func handleSession(wtSess *webtransport.Session, cm *certManager) {
	ctx := wtSess.Context()

	sub := &subscriber{sess: wtSess}

	stream, err := wtSess.AcceptStream(ctx)
	if err != nil {
		return
	}
	sub.ctrl = stream

	// If a stream is already running, subscribe this new client
	stateMu.Lock()
	if state != nil {
		state.subMu.Lock()
		state.subscribers[sub] = struct{}{}
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
				teardown()
			}
		}
		stateMu.Unlock()
	}()

	// Send current fingerprint on connect
	if cm != nil {
		sendControlMsg(sub, map[string]any{
			"type":        "fingerprint-refresh",
			"algorithm":   "sha-256",
			"fingerprint": cm.FingerprintHex(),
		})
	}

	dec := json.NewDecoder(sub.ctrl)
	for {
		var req struct {
			Type      string `json:"type"`
			DisplayID uint32 `json:"display_id,omitempty"`
			FPS       int    `json:"fps,omitempty"`
			Codec     string `json:"codec,omitempty"`
			Bitrate   int    `json:"bitrate,omitempty"`
		}
		if err := dec.Decode(&req); err != nil {
			break
		}

		switch req.Type {
		case "list-displays":
			displays, err := listDisplays()
			if err != nil {
				sendControlMsg(sub, map[string]string{"type": "error", "message": err.Error()})
				continue
			}
			sendControlMsg(sub, map[string]any{"type": "displays", "displays": displays})

		case "start":
			if req.FPS == 0 {
				req.FPS = 60
			}
			if req.Codec == "" {
				req.Codec = "h264"
			}
			if err := startStream(int(req.DisplayID), req.FPS, req.Codec, req.Bitrate, sub); err != nil {
				sendControlMsg(sub, map[string]string{"type": "error", "message": err.Error()})
				continue
			}
			stateMu.Lock()
			w, h := state.width, state.height
			stateMu.Unlock()
			sendControlMsg(sub, map[string]any{
				"type":   "started",
				"width":  w,
				"height": h,
				"codec":  req.Codec,
			})

		case "stop":
			stateMu.Lock()
			if state != nil {
				teardown()
			}
			stateMu.Unlock()
			sendControlMsg(sub, map[string]string{"type": "stopped"})

		default:
			sendControlMsg(sub, map[string]string{"type": "error", "message": fmt.Sprintf("unknown type: %s", req.Type)})
		}
	}
}

// -- captured interaction --

func capturedSocket() string {
	if v := os.Getenv("CAPTURED_SOCKET"); v != "" {
		return v
	}
	return "/tmp/captured.socket"
}

func listDisplays() ([]map[string]any, error) {
	conn, err := net.Dial("unix", capturedSocket())
	if err != nil {
		return nil, fmt.Errorf("captured: %w", err)
	}
	defer conn.Close()
	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	if err := enc.Encode(map[string]string{"type": "list-displays"}); err != nil {
		return nil, err
	}
	var resp struct {
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
	if err := dec.Decode(&resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, errors.New(resp.Error)
	}
	out := make([]map[string]any, len(resp.Displays))
	for i, d := range resp.Displays {
		out[i] = map[string]any{
			"id":           d.ID,
			"width":        d.Width,
			"height":       d.Height,
			"x":            d.X,
			"y":            d.Y,
			"refresh_rate": d.RefreshRate,
		}
	}
	return out, nil
}

// -- streaming --

func startStream(displayID, fps int, codec string, bitrate int, caller *subscriber) error {
	stateMu.Lock()
	defer stateMu.Unlock()

	if state != nil {
		videoStream, err := caller.sess.OpenUniStream()
		if err != nil {
			return fmt.Errorf("video stream: %w", err)
		}
		caller.video = videoStream
		state.subMu.Lock()
		state.subscribers[caller] = struct{}{}
		state.subMu.Unlock()
		return nil
	}

	ctrl, err := net.Dial("unix", capturedSocket())
	if err != nil {
		return fmt.Errorf("captured control: %w", err)
	}

	enc := json.NewEncoder(ctrl)
	dec := json.NewDecoder(ctrl)

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
		ctrl.Close()
		return errors.New(streamResp.Error)
	}

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
		json.NewEncoder(state.capturedCtrl).Encode(map[string]string{"type": "stop-stream"})
		state.capturedCtrl.Close()
	}
	state.subMu.Lock()
	for sub := range state.subscribers {
		sendControlMsg(sub, map[string]string{"type": "stream-ended"})
		if sub.video != nil {
			sub.video.Close()
		}
		sub.sess.CloseWithError(0, "stream ended")
	}
	state.subscribers = nil
	state.subMu.Unlock()
	state = nil
}
