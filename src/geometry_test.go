package main

import (
	"context"
	"errors"
	"testing"

	"distancedesktop/agent/src/backend"
)

type geometryBackend struct {
	t        *testing.T
	displays []backend.Display
	err      error
}

func (*geometryBackend) Name() string { return "geometry-test" }
func (b *geometryBackend) ListDisplays(context.Context) ([]backend.Display, error) {
	if !stateMu.TryLock() {
		b.t.Fatal("ListDisplays called with stateMu held")
	}
	stateMu.Unlock()
	return b.displays, b.err
}
func (*geometryBackend) StartStream(context.Context, backend.StartRequest) (backend.Stream, error) {
	return nil, errors.New("test startup failure")
}
func TestGeometryLookupOutsideStateLock(t *testing.T) {
	previous := activeBackend
	defer func() { activeBackend = previous }()
	for _, b := range []*geometryBackend{
		{t: t, displays: []backend.Display{{ID: 2, X: -100, Y: 30, Width: 100, Height: 80}}},
		{t: t}, {t: t, err: errors.New("probe failed")},
	} {
		activeBackend = b
		if err := startStream(2, 60, "h264", 0, nil); err == nil {
			t.Fatal("expected startup error")
		}
	}
	g := displayGeometry(&geometryBackend{t: t, displays: []backend.Display{{ID: 2, X: -100, Y: 30, Width: 100, Height: 80}}}, 2)
	if g.X != -100 || g.Y != 30 || g.Width != 100 || g.Height != 80 {
		t.Fatalf("geometry: %+v", g)
	}
}
