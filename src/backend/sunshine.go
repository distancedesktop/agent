package backend

// Backend "sunshine": connects to a Sunshine host (Moonlight protocol) and
// passes through its H.264 Annex B directly — no local ffmpeg.
//
// Protocol study reference: .tmp/eval/moonlight-web-stream (Rust) and its
// moonlight-common fork. The Moonlight handshake is:
//
//	1. HTTPS to :47989 — /serverinfo gives host info + RTSP session count.
//	2. HTTP /launch (or /resume) with query args: uniqueid, uuid,
//	   rikeyid, rikey (symmetric AES key, hex), remoteAudioEnabled,
//	   supportedDisplayModes, mode WxHxFPS, sops, etc. Response is XML
//	   <root><status_code>200</...><sessionurl0>...</sessionurl0></root>.
//	3. RTSP to :48010, port mode 53000-ish: OPTIONS -> DESCRIBE ->
//	   SETUP (aggregated or per-track, client keeps rikey for SRTP AES-GCM)
//	   -> PLAY. Video arrives over UDP with enroll/feedback channels; H264
//	   payload needs NAL reassembly from Moonlight's framed packets into
//	   Annex B before handoff to us.
//	4. Control channel on :47999 (TCP) carries input events + keepalives.
//
// v1 scope here: reachability probe of 47989 and an explicit
// ErrNotImplemented until the RTSP/SRTP handshake + NAL reassembly lands
// (Spike B follow-up).

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
)

func init() { Register(&SunshineBackend{}) }

// ErrNotImplemented is returned by backend stubs whose full protocol work
// has not landed yet.
var ErrNotImplemented = errors.New("backend not implemented yet")

const (
	sunshineHTTPPort  = 47989 // HTTPS serverinfo/launch/resume/cancel
	sunshineRTSPPort  = 48010 // RTSP control
	sunshineCtrlPort  = 47999 // TCP control/input
	sunshineVideoPort = 47998 // UDP video
	sunshineAudioPort = 48000 // UDP audio
)

// SunshineBackend streams a Sunshine/Moonlight host's H264 passthrough.
type SunshineBackend struct {
	// Addr is host[:port] of the Sunshine host; default port 47989.
	Addr string
}

func (b *SunshineBackend) Name() string { return "sunshine" }

func (b *SunshineBackend) httpAddr() string {
	host := b.Addr
	if host == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		return net.JoinHostPort(host, strconv.Itoa(sunshineHTTPPort))
	}
	return host
}

// ListDisplays probes the Sunshine HTTP endpoint. Full enumeration requires
// the /serverinfo handshake (see package comment); until then we only verify
// reachability so `--dry-run` diagnostics mean something.
func (b *SunshineBackend) ListDisplays(ctx context.Context) ([]Display, error) {
	addr := b.httpAddr()
	if addr == "" {
		return nil, fmt.Errorf("sunshine: no --sunshine address configured")
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("sunshine: probe %s: %w", addr, err)
	}
	conn.Close()
	log.Printf("sunshine: %s reachable (HTTP %d); full display list pending RTSP handshake implementation", addr, sunshineHTTPPort)
	return nil, ErrNotImplemented
}

// StartStream performs the launch+RTSP+PLAY flow; see package comment.
func (b *SunshineBackend) StartStream(ctx context.Context, req StartRequest) (Stream, error) {
	if _, err := b.ListDisplays(ctx); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("sunshine: %w (launch/RTSP/NAL-reassembly not yet implemented)", ErrNotImplemented)
}

// tcpProbe dials host:port once for reachability checks in stubs.
func tcpProbe(ctx context.Context, addr string, defPort int) error {
	if addr == "" {
		return fmt.Errorf("no address configured")
	}
	target := addr
	if !strings.Contains(addr, ":") || strings.Count(addr, ":") == 0 {
		target = net.JoinHostPort(addr, strconv.Itoa(defPort))
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		return fmt.Errorf("probe %s: %w", target, err)
	}
	conn.Close()
	return nil
}
