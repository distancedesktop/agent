package backend

// Backend "rdp": connects to an RDP server (MS-RDPBCGR), negotiates a
// graphics channel and re-encodes updates to H264. Stub: TCP reachability
// only. Full MS-RDPBCGR handshake is large; eval freerdp/librdp bindings
// before hand-rolling.

import (
	"context"
	"fmt"
	"log"
)

func init() { Register(&RDPBackend{}) }

const rdpDefaultPort = 3389

// RDPBackend captures a remote RDP session.
type RDPBackend struct {
	// Addr is host[:port] of the RDP server; default port 3389.
	Addr string
}

func (b *RDPBackend) Name() string { return "rdp" }

// ListDisplays verifies TCP reachability on the RDP port.
func (b *RDPBackend) ListDisplays(ctx context.Context) ([]Display, error) {
	if b.Addr == "" {
		return nil, fmt.Errorf("rdp: no --rdp address configured")
	}
	if err := tcpProbe(ctx, b.Addr, rdpDefaultPort); err != nil {
		return nil, fmt.Errorf("rdp: %w", err)
	}
	log.Printf("rdp: %s reachable; MS-RDPBCGR handshake pending implementation", b.Addr)
	return nil, ErrNotImplemented
}

// StartStream is not implemented yet.
func (b *RDPBackend) StartStream(ctx context.Context, req StartRequest) (Stream, error) {
	if _, err := b.ListDisplays(ctx); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("rdp: %w (MS-RDPBCGR not yet implemented)", ErrNotImplemented)
}
