//go:build linux

package input

import (
	"fmt"
	"math"
	"os"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

var xOnce sync.Once
var xErr error
var openDisplay func(string) unsafe.Pointer
var closeDisplay func(unsafe.Pointer) int32
var flush func(unsafe.Pointer) int32
var motion func(unsafe.Pointer, int32, int32, int32, uintptr) int32
var relative func(unsafe.Pointer, int32, int32, uintptr) int32
var button func(unsafe.Pointer, uint32, int32, uintptr) int32
var key func(unsafe.Pointer, uint32, int32, uintptr) int32
var query func(unsafe.Pointer, *int32, *int32, *int32, *int32) int32

type native struct {
	d      unsafe.Pointer
	wx, wy float64
}

func New() (Injector, error) {
	if os.Getenv("DISPLAY") == "" {
		return nil, fmt.Errorf("input unavailable: DISPLAY is unset")
	}
	xOnce.Do(func() {
		x, err := purego.Dlopen("libX11.so.6", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			xErr = err
			return
		}
		t, err := purego.Dlopen("libXtst.so.6", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			xErr = err
			return
		}
		var initThreads func() int32
		purego.RegisterLibFunc(&initThreads, x, "XInitThreads")
		if initThreads() == 0 {
			xErr = fmt.Errorf("XInitThreads failed")
			return
		}
		purego.RegisterLibFunc(&openDisplay, x, "XOpenDisplay")
		purego.RegisterLibFunc(&closeDisplay, x, "XCloseDisplay")
		purego.RegisterLibFunc(&flush, x, "XFlush")
		purego.RegisterLibFunc(&motion, t, "XTestFakeMotionEvent")
		purego.RegisterLibFunc(&relative, t, "XTestFakeRelativeMotionEvent")
		purego.RegisterLibFunc(&button, t, "XTestFakeButtonEvent")
		purego.RegisterLibFunc(&key, t, "XTestFakeKeyEvent")
		purego.RegisterLibFunc(&query, t, "XTestQueryExtension")
	})
	if xErr != nil {
		return nil, fmt.Errorf("input unavailable: %w", xErr)
	}
	d := openDisplay(os.Getenv("DISPLAY"))
	if d == nil {
		return nil, fmt.Errorf("input unavailable: cannot open DISPLAY %q", os.Getenv("DISPLAY"))
	}
	var a, b, c, e int32
	if query(d, &a, &b, &c, &e) == 0 {
		closeDisplay(d)
		return nil, fmt.Errorf("input unavailable: XTEST extension missing")
	}
	return &native{d: d}, nil
}
func truth(b bool) int32 {
	if b {
		return 1
	}
	return 0
}
func (n *native) done(ok int32) error {
	flush(n.d)
	if ok == 0 {
		return fmt.Errorf("XTest injection failed")
	}
	return nil
}
func (n *native) Mouse(x, y int) error { return n.done(relative(n.d, int32(x), int32(y), 0)) }
func (n *native) Move(x, y int) error  { return n.done(motion(n.d, -1, int32(x), int32(y), 0)) }
func (n *native) Button(b int, down bool) error {
	if b < 0 || b > 4 {
		return fmt.Errorf("invalid button")
	}
	return n.done(button(n.d, []uint32{1, 2, 3, 8, 9}[b], truth(down), 0))
}
func (n *native) Key(code string, down bool) error {
	scan, ok := EvdevCode(code)
	if !ok {
		return fmt.Errorf("unsupported key %q", code)
	}
	return n.done(key(n.d, uint32(scan)+8, truth(down), 0))
}
func (n *native) Wheel(dx, dy float64) error {
	n.wx += dx / 40
	n.wy += dy / 40
	for _, a := range []struct {
		v        *float64
		neg, pos uint32
	}{{&n.wx, 6, 7}, {&n.wy, 4, 5}} {
		for math.Abs(*a.v) >= 1 {
			b := a.pos
			if *a.v < 0 {
				b = a.neg
				*a.v++
			} else {
				*a.v--
			}
			if err := n.done(button(n.d, b, 1, 0)); err != nil {
				return err
			}
			if err := n.done(button(n.d, b, 0, 0)); err != nil {
				return err
			}
		}
	}
	return nil
}
func (n *native) Close() error { closeDisplay(n.d); return nil }
