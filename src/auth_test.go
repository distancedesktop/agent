package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/okdaichi/webtransport-go"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"distancedesktop/agent/src/backend"
)

const testToken = "test-viewer-token-0123456789"

// fakeBackend streams a repeating SPS/PPS/IDR access unit until closed.
type fakeBackend struct{}

func (fakeBackend) Name() string { return "fake" }

func (fakeBackend) ListDisplays(context.Context) ([]backend.Display, error) {
	return []backend.Display{{ID: 0, Width: 64, Height: 32, RefreshRate: 60}}, nil
}

func (fakeBackend) StartStream(context.Context, backend.StartRequest) (backend.Stream, error) {
	s := &fakeStream{ch: make(chan backend.H264Chunk), done: make(chan struct{})}
	go func() {
		defer close(s.ch)
		au := []byte{0, 0, 0, 1, 0x67, 0x42, 0, 0, 0, 1, 0x68, 0xce, 0, 0, 0, 1, 0x65, 0x88, 0x84}
		t := time.NewTicker(10 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-t.C:
				select {
				case s.ch <- backend.H264Chunk{Data: au, Keyframe: true}:
				case <-s.done:
					return
				}
			}
		}
	}()
	return s, nil
}

type fakeStream struct {
	ch   chan backend.H264Chunk
	done chan struct{}
}

func (s *fakeStream) Chunks() <-chan backend.H264Chunk { return s.ch }
func (s *fakeStream) Width() int                       { return 64 }
func (s *fakeStream) Height() int                      { return 32 }
func (s *fakeStream) FPS() int                         { return 60 }
func (s *fakeStream) Codec() string                    { return "h264" }
func (s *fakeStream) Close() error {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	return nil
}

// startTestAgent serves the real /wt handler on a random UDP port and returns
// its base URL.
func startTestAgent(t *testing.T) string {
	t.Helper()
	activeBackend = fakeBackend{}
	cm, err := newCertManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	up := &webtransport.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/wt", wtHandler(up, cm, testToken))
	srv := &webtransport.Server{H3: &http3.Server{
		TLSConfig:  cm.TLSConfig(),
		QUICConfig: &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true},
		Handler:    mux,
	}}
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(conn)
	t.Cleanup(func() {
		stateMu.Lock()
		teardown()
		stateMu.Unlock()
		srv.Close()
		conn.Close()
	})
	return "https://" + conn.LocalAddr().String() + "/wt"
}

func dial(t *testing.T, ctx context.Context, rawURL string, hdr http.Header) (*http.Response, *webtransport.Session, error) {
	t.Helper()
	d := &webtransport.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	t.Cleanup(func() { d.Close() })
	return d.Dial(ctx, rawURL, hdr)
}

func TestUnauthenticatedViewerRejected(t *testing.T) {
	base := startTestAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for name, tc := range map[string]struct {
		url string
		hdr http.Header
	}{
		"no token":     {base, nil},
		"wrong token":  {base + "?token=" + url.QueryEscape(testToken+"x"), nil},
		"empty token":  {base + "?token=", nil},
		"wrong bearer": {base, http.Header{"Authorization": {"Bearer nope"}}},
	} {
		t.Run(name, func(t *testing.T) {
			rsp, sess, err := dial(t, ctx, tc.url, tc.hdr)
			if err == nil {
				sess.CloseWithError(0, "")
				t.Fatal("upgrade succeeded without a valid token")
			}
			if rsp == nil || rsp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("want 401, got rsp=%v err=%v", rsp, err)
			}
		})
	}
}

func TestAuthenticatedViewerStreams(t *testing.T) {
	base := startTestAgent(t)
	for name, tc := range map[string]struct {
		url string
		hdr http.Header
	}{
		"query token":  {base + "?token=" + url.QueryEscape(testToken), nil},
		"bearer token": {base, http.Header{"Authorization": {"Bearer " + testToken}}},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			rsp, sess, err := dial(t, ctx, tc.url, tc.hdr)
			if err != nil {
				t.Fatalf("dial: rsp=%v err=%v", rsp, err)
			}
			defer sess.CloseWithError(0, "")

			ctrl, err := sess.OpenStreamSync(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ctrl.Write([]byte(`{"type":"start","display_id":0}` + "\n")); err != nil {
				t.Fatal(err)
			}
			deadline, _ := ctx.Deadline()
			ctrl.SetReadDeadline(deadline)
			msgs := bufio.NewScanner(ctrl)
			var started bool
			for !started && msgs.Scan() {
				var m struct{ Type string }
				if err := json.Unmarshal(msgs.Bytes(), &m); err != nil {
					t.Fatalf("bad control message %q: %v", msgs.Text(), err)
				}
				started = m.Type == "started"
			}
			if !started {
				t.Fatalf("no started message: %v", msgs.Err())
			}

			video, err := sess.AcceptUniStream(ctx)
			if err != nil {
				t.Fatal(err)
			}
			video.SetReadDeadline(deadline)
			var hdr [13]byte
			if _, err := io.ReadFull(video, hdr[:]); err != nil {
				t.Fatalf("read video record: %v", err)
			}
			n := binary.BigEndian.Uint32(hdr[9:])
			payload := make([]byte, n)
			if _, err := io.ReadFull(video, payload); err != nil {
				t.Fatalf("read video payload: %v", err)
			}
			if hdr[0]&1 == 0 || !keyframe(payload) {
				t.Fatalf("expected a keyframe record, got flags=%#x payload=%x", hdr[0], payload)
			}

			stateMu.Lock()
			teardown()
			stateMu.Unlock()
		})
	}
}

func TestLoadOrCreateToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "viewer-token")
	tok, err := loadOrCreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) < minTokenLen {
		t.Fatalf("generated token too short: %q", tok)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v, want 0600", fi.Mode().Perm())
	}
	again, err := loadOrCreateToken(path)
	if err != nil || again != tok {
		t.Fatalf("reload: got %q, %v; want %q", again, err, tok)
	}

	short := filepath.Join(t.TempDir(), "short")
	os.WriteFile(short, []byte("abc\n"), 0o600)
	if _, err := loadOrCreateToken(short); err == nil {
		t.Fatal("accepted a too-short token")
	}

	if runtime.GOOS != "windows" {
		loose := filepath.Join(t.TempDir(), "loose")
		os.WriteFile(loose, []byte("0123456789abcdef0123\n"), 0o644)
		os.Chmod(loose, 0o644)
		if _, err := loadOrCreateToken(loose); err == nil {
			t.Fatal("accepted a group/world-readable token file")
		}
	}
	if _, err := loadOrCreateToken(t.TempDir()); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory as token file: got %v", err)
	}
}
