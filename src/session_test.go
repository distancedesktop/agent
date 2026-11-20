package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/okdaichi/webtransport-go"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"distancedesktop/agent/src/backend"
	"distancedesktop/agent/src/input"
)

type stopBackend struct {
	mu      sync.Mutex
	streams []*stopStream
}

func (b *stopBackend) Name() string { return "fake" }

func (b *stopBackend) ListDisplays(context.Context) ([]backend.Display, error) {
	return []backend.Display{{ID: 0, Width: 64, Height: 64, RefreshRate: 60}}, nil
}

func (b *stopBackend) StartStream(context.Context, backend.StartRequest) (backend.Stream, error) {
	s := &stopStream{chunks: make(chan backend.H264Chunk), done: make(chan struct{}), keyframes: make(chan struct{}, 1)}
	go s.run()
	b.mu.Lock()
	b.streams = append(b.streams, s)
	b.mu.Unlock()
	return s, nil
}

func (b *stopBackend) started() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.streams)
}

type stopStream struct {
	chunks    chan backend.H264Chunk
	done      chan struct{}
	closeOnce sync.Once
	keyframes chan struct{}
}

func (s *stopStream) run() {
	defer close(s.chunks)
	key := []byte{0, 0, 0, 1, 0x67, 0x42, 0x00, 0, 0, 1, 0x68, 0xce, 0x06, 0, 0, 0, 1, 0x65, 0x88, 0x11}
	slice := []byte{0, 0, 1, 0x41, 0x9a, 0x22}
	next := key
	t := time.NewTicker(5 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case s.chunks <- backend.H264Chunk{Data: next}:
			next = slice
		case <-s.keyframes:
			next = key
		case <-s.done:
			return
		}
		select {
		case <-t.C:
		case <-s.done:
			return
		}
	}
}

func (s *stopStream) Chunks() <-chan backend.H264Chunk { return s.chunks }
func (s *stopStream) RequestKeyframe() {
	select {
	case s.keyframes <- struct{}{}:
	default:
	}
}
func (s *stopStream) Width() int    { return 64 }
func (s *stopStream) Height() int   { return 64 }
func (s *stopStream) FPS() int      { return 60 }
func (s *stopStream) Codec() string { return "h264" }
func (s *stopStream) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	return nil
}

func (s *stopStream) closed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// startStopTestAgent serves handleSession on a loopback WebTransport server backed
// by a fake backend and returns the /wt URL.
func startStopTestAgent(t *testing.T) (string, *stopBackend) {
	t.Helper()
	fb := &stopBackend{}
	prev := activeBackend
	activeBackend = fb
	cm, err := newCertManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	up := &webtransport.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/wt", func(w http.ResponseWriter, r *http.Request) {
		s, err := up.Upgrade(w, r)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		go handleSession(s, nil)
	})
	srv := &webtransport.Server{H3: &http3.Server{
		TLSConfig:  cm.TLSConfig(),
		QUICConfig: &quic.Config{EnableDatagrams: true},
		Handler:    mux,
	}}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(conn)
	t.Cleanup(func() {
		// Viewers have disconnected by now; the owner's exit tears the stream down.
		deadline := time.Now().Add(5 * time.Second)
		for streamActive() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if streamActive() {
			t.Error("stream still active after all viewers disconnected")
		}
		srv.Close()
		conn.Close()
		activeBackend = prev
	})
	return "https://" + conn.LocalAddr().String() + "/wt", fb
}

type testViewer struct {
	t     *testing.T
	sess  *webtransport.Session
	ctrl  *webtransport.Stream
	msgs  chan map[string]any
	video *webtransport.ReceiveStream
}

func dialViewer(t *testing.T, url string) *testViewer {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d := &webtransport.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, NextProtos: []string{http3.NextProtoH3}}}
	t.Cleanup(func() { d.Close() })
	_, sess, err := d.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	ctrl, err := sess.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("open control: %v", err)
	}
	v := &testViewer{t: t, sess: sess, ctrl: ctrl, msgs: make(chan map[string]any, 64)}
	t.Cleanup(func() { sess.CloseWithError(0, "") })
	go func() {
		defer close(v.msgs)
		dec := json.NewDecoder(ctrl)
		for {
			var m map[string]any
			if err := dec.Decode(&m); err != nil {
				return
			}
			v.msgs <- m
		}
	}()
	v.send("list-displays")
	v.expect("displays")
	return v
}

func (v *testViewer) send(typ string) {
	v.t.Helper()
	if err := json.NewEncoder(v.ctrl).Encode(map[string]any{"type": typ}); err != nil {
		v.t.Fatalf("send %s: %v", typ, err)
	}
}

// expect waits for a control message of type typ, failing on stream-ended
// unless that is what is expected.
func (v *testViewer) expect(typ string) {
	v.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case m, ok := <-v.msgs:
			if !ok {
				v.t.Fatalf("control stream closed waiting for %q", typ)
			}
			got, _ := m["type"].(string)
			if got == typ {
				return
			}
			if got == "stream-ended" || got == "error" {
				v.t.Fatalf("got %v waiting for %q", m, typ)
			}
		case <-timeout:
			v.t.Fatalf("timed out waiting for %q", typ)
		}
	}
}

// expectNo fails if a control message of type typ arrives within d.
func (v *testViewer) expectNo(typ string, d time.Duration) {
	v.t.Helper()
	timeout := time.After(d)
	for {
		select {
		case m, ok := <-v.msgs:
			if !ok {
				v.t.Fatalf("control stream closed")
			}
			if m["type"] == typ {
				v.t.Fatalf("unexpected %q", typ)
			}
		case <-timeout:
			return
		}
	}
}

func (v *testViewer) start() {
	v.t.Helper()
	v.send("start")
	v.expect("started")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	vs, err := v.sess.AcceptUniStream(ctx)
	if err != nil {
		v.t.Fatalf("accept video: %v", err)
	}
	v.video = vs
}

// readVideo reads at least n bytes of video, failing on EOF or timeout.
func (v *testViewer) readVideo(n int) {
	v.t.Helper()
	v.video.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(v.video, make([]byte, n)); err != nil {
		v.t.Fatalf("read video: %v", err)
	}
}

// expectVideoEnd drains video until it ends, failing if it is still flowing.
func (v *testViewer) expectVideoEnd() {
	v.t.Helper()
	v.video.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err := io.Copy(io.Discard, v.video)
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		v.t.Fatalf("video still open")
	}
}

func streamActive() bool {
	stateMu.Lock()
	defer stateMu.Unlock()
	return state != nil
}

func TestNonOwnerStopOnlyDetachesThatViewer(t *testing.T) {
	url, fb := startStopTestAgent(t)
	owner := dialViewer(t, url)
	owner.start()
	viewer := dialViewer(t, url)
	viewer.start()
	viewer.readVideo(64)

	viewer.send("stop")
	viewer.expect("stopped")
	viewer.expectVideoEnd()

	owner.expectNo("stream-ended", 200*time.Millisecond)
	owner.readVideo(4096)
	if !streamActive() {
		t.Fatal("stream torn down by non-owner stop")
	}
	if fb.streams[0].closed() {
		t.Fatal("backend stream closed by non-owner stop")
	}

	// The viewer can rejoin the still-running stream.
	viewer.start()
	viewer.readVideo(64)
	if n := fb.started(); n != 1 {
		t.Fatalf("backend started %d streams, want 1", n)
	}
}

func TestOwnerStopEndsStreamForEveryone(t *testing.T) {
	url, fb := startStopTestAgent(t)
	owner := dialViewer(t, url)
	owner.start()
	viewer := dialViewer(t, url)
	viewer.start()
	viewer.readVideo(64)

	owner.send("stop")
	owner.expect("stopped")
	viewer.expect("stream-ended")
	viewer.expectVideoEnd()
	if streamActive() {
		t.Fatal("stream still active after owner stop")
	}
	if !fb.streams[0].closed() {
		t.Fatal("backend stream not closed after owner stop")
	}
}

type heldInputRecorder struct {
	mu                        sync.Mutex
	presses, releases, closes int
}

func (*heldInputRecorder) Mouse(int, int) error         { return nil }
func (*heldInputRecorder) Move(int, int) error          { return nil }
func (*heldInputRecorder) Button(int, bool) error       { return nil }
func (*heldInputRecorder) Wheel(float64, float64) error { return nil }
func (i *heldInputRecorder) Key(_ string, down bool) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if down {
		i.presses++
	} else {
		i.releases++
	}
	return nil
}
func (i *heldInputRecorder) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.closes++
	return nil
}
func (i *heldInputRecorder) counts() (int, int, int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.presses, i.releases, i.closes
}

func TestNonOwnerStopPreservesOwnersHeldInput(t *testing.T) {
	recorder := &heldInputRecorder{}
	previous := newInjector
	newInjector = func() (input.Injector, error) { return recorder, nil }
	t.Cleanup(func() { newInjector = previous })
	url, _ := startStopTestAgent(t)
	owner := dialViewer(t, url)
	owner.start()
	viewer := dialViewer(t, url)
	viewer.start()
	if err := json.NewEncoder(owner.ctrl).Encode(map[string]any{
		"type": "input", "kind": "key", "code": "KeyA", "down": true,
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		presses, _, _ := recorder.counts()
		if presses == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owner key press not injected")
		}
		time.Sleep(time.Millisecond)
	}
	stateMu.Lock()
	controller := state.input
	stateMu.Unlock()
	viewer.send("stop")
	viewer.expect("stopped")
	viewer.expectVideoEnd()
	stateMu.Lock()
	preserved := state != nil && state.input == controller
	stateMu.Unlock()
	if !preserved {
		t.Fatal("non-owner stop replaced the owner's injector")
	}
	if _, releases, closes := recorder.counts(); releases != 0 || closes != 0 {
		t.Fatalf("non-owner stop released input: releases=%d closes=%d", releases, closes)
	}
	owner.send("stop")
	owner.expect("stopped")
	if _, releases, closes := recorder.counts(); releases != 1 || closes != 1 {
		t.Fatalf("owner stop did not release input: releases=%d closes=%d", releases, closes)
	}
}
