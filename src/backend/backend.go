// Package backend defines the pluggable capture/stream backend interface
// for the distance agent.
//
// A Backend lists displays and produces an H.264 Annex B chunk stream that
// the WebTransport layer fans out to subscribers. Concrete backends live in
// this package (captured, sunshine, vnc, rdp) and self-register via init().
package backend

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Display describes one capturable output.
type Display struct {
	ID          uint32  `json:"id"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	X           int     `json:"x"`
	Y           int     `json:"y"`
	RefreshRate float64 `json:"refresh_rate"`
}

// StartRequest parameterizes a stream start.
type StartRequest struct {
	DisplayID uint32
	FPS       int
	Codec     string // h264 | hevc | av1 | vp9
	Bitrate   int    // bits/sec, 0 = backend default
}

// H264Chunk is a piece of H.264 Annex B data ready for transport.
type H264Chunk struct {
	Data     []byte
	Keyframe bool
}

// Stream is a live video stream from a backend.
type Stream interface {
	// Chunks yields encoded H.264 chunks until the stream ends, then closes.
	Chunks() <-chan H264Chunk
	Width() int
	Height() int
	FPS() int
	Codec() string
	// Close tears down capture + encode; idempotent.
	Close() error
}

// Backend is a pluggable video source.
type Backend interface {
	Name() string
	ListDisplays(ctx context.Context) ([]Display, error)
	StartStream(ctx context.Context, req StartRequest) (Stream, error)
}

var (
	regMu     sync.Mutex
	registry  = map[string]Backend{}
	AutoOrder = []string{"captured", "sunshine", "vnc", "rdp"}
)

// Register adds a backend to the registry. Later registration of the same
// name replaces the earlier entry.
func Register(b Backend) {
	regMu.Lock()
	defer regMu.Unlock()
	registry[b.Name()] = b
}

// Get returns the named backend.
func Get(name string) (Backend, error) {
	regMu.Lock()
	defer regMu.Unlock()
	b, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("backend: unknown backend %q (available: %v)", name, namesSorted())
	}
	return b, nil
}

// Names returns registered backend names sorted.
func Names() []string {
	regMu.Lock()
	defer regMu.Unlock()
	return namesSorted()
}

// namesSorted returns the sorted list of registered backend names.
// Must be called with regMu held.
func namesSorted() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// AutoCandidate is one AutoOrder step result used by callers implementing
// `--backend auto`.
type AutoCandidate struct {
	Name    string
	Backend Backend
}

// Candidates returns backends in auto-probing order (registration order is
// irrelevant; AutoOrder wins).
func Candidates() []AutoCandidate {
	regMu.Lock()
	defer regMu.Unlock()
	var out []AutoCandidate
	for _, name := range AutoOrder {
		if b, ok := registry[name]; ok {
			out = append(out, AutoCandidate{Name: name, Backend: b})
		}
	}
	return out
}
