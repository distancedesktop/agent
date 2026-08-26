package backend

// Backend "vnc": dials a VNC server (RFB protocol), requests continuous
// frame updates (SetContinuousUpdates / FramebufferUpdateRequest) and
// re-encodes frames to H264. Stub: reachability + RFB banner check only.

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"strconv"
)

func init() { Register(&VNCBackend{}) }

const vncDefaultPort = 5900

// VNCBackend captures a remote VNC display.
type VNCBackend struct {
	// Addr is host[:port] of the VNC server; default port 5900.
	Addr string
}

func (b *VNCBackend) Name() string { return "vnc" }

func (b *VNCBackend) addr() string {
	if b.Addr == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(b.Addr); err != nil {
		return net.JoinHostPort(b.Addr, strconv.Itoa(vncDefaultPort))
	}
	return b.Addr
}

// ListDisplays connects and reads the RFB handshake banner ("RFB xxx.y").
func (b *VNCBackend) ListDisplays(ctx context.Context) ([]Display, error) {
	addr := b.addr()
	if addr == "" {
		return nil, fmt.Errorf("vnc: no --vnc address configured")
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("vnc: %w", err)
	}
	defer conn.Close()
	banner := make([]byte, 12)
	n, err := bufio.NewReader(conn).Read(banner)
	if err != nil {
		log.Printf("vnc: banner read (%d bytes): %v", n, err)
	}
	if n >= 3 && string(banner[:3]) == "RFB" {
		log.Printf("vnc: %s speaks %q; RFB capture pending implementation", addr, banner)
		return nil, ErrNotImplemented
	}
	return nil, fmt.Errorf("vnc: not an RFB server at %s", addr)
}

// StartStream is not implemented yet (needs RFB pixel decode + encode).
func (b *VNCBackend) StartStream(ctx context.Context, req StartRequest) (Stream, error) {
	if _, err := b.ListDisplays(ctx); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("vnc: %w (RFB frame polling not yet implemented)", ErrNotImplemented)
}
