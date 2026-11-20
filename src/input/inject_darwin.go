//go:build darwin

package input

import (
	"fmt"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

var macOnce sync.Once
var macErr error
var createKey func(unsafe.Pointer, uint16, bool) unsafe.Pointer
var createMouse func(unsafe.Pointer, uint32, point, uint32) unsafe.Pointer
var createEvent func(unsafe.Pointer) unsafe.Pointer
var getLocation func(unsafe.Pointer) point
var post func(uint32, unsafe.Pointer)
var release func(unsafe.Pointer)
var scroll func(unsafe.Pointer, uint32, uint32, int32, int32, int32) unsafe.Pointer
var trusted func() bool
var setIntegerValue func(unsafe.Pointer, uint32, int64)
var setFlags func(unsafe.Pointer, uint64)

type point struct{ X, Y float64 }
type native struct {
	modifiers   macModifiers
	buttons     [5]bool
	clicks      clickTracker
	clickCounts [5]int64
}

func New() (Injector, error) {
	macOnce.Do(func() {
		h, err := purego.Dlopen("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			macErr = err
			return
		}
		purego.RegisterLibFunc(&createKey, h, "CGEventCreateKeyboardEvent")
		purego.RegisterLibFunc(&createMouse, h, "CGEventCreateMouseEvent")
		purego.RegisterLibFunc(&createEvent, h, "CGEventCreate")
		purego.RegisterLibFunc(&getLocation, h, "CGEventGetLocation")
		purego.RegisterLibFunc(&post, h, "CGEventPost")
		purego.RegisterLibFunc(&release, h, "CFRelease")
		purego.RegisterLibFunc(&scroll, h, "CGEventCreateScrollWheelEvent2")
		purego.RegisterLibFunc(&trusted, h, "AXIsProcessTrusted")
		purego.RegisterLibFunc(&setIntegerValue, h, "CGEventSetIntegerValueField")
		purego.RegisterLibFunc(&setFlags, h, "CGEventSetFlags")
	})
	if macErr != nil {
		return nil, macErr
	}
	if !trusted() {
		return nil, fmt.Errorf("input unavailable: grant Accessibility permission to the agent")
	}
	return &native{}, nil
}
func (n *native) emit(e unsafe.Pointer) error {
	if e == nil {
		return fmt.Errorf("CGEvent creation failed")
	}
	setFlags(e, n.modifiers.flags())
	post(0, e)
	release(e)
	return nil
}
func location() point {
	e := createEvent(nil)
	if e == nil {
		return point{}
	}
	defer release(e)
	return getLocation(e)
}
func (n *native) Mouse(x, y int) error { p := location(); return n.Move(int(p.X)+x, int(p.Y)+y) }
func (n *native) Move(x, y int) error {
	kind, b := uint32(5), uint32(0)
	if n.buttons[0] {
		kind = 6
	} else if n.buttons[2] {
		kind, b = 7, 1
	} else if n.buttons[1] {
		kind, b = 27, 2
	}
	return n.emit(createMouse(nil, kind, point{float64(x), float64(y)}, b))
}
func (n *native) Button(b int, down bool) error {
	if b < 0 || b > 4 {
		return fmt.Errorf("invalid button")
	}
	p := location()
	cb := []uint32{0, 2, 1, 3, 4}[b]
	kind := uint32(25)
	if b == 0 {
		kind = 1
	} else if b == 2 {
		kind = 3
	}
	if !down {
		kind++
	}
	e := createMouse(nil, kind, p, cb)
	if e == nil {
		return fmt.Errorf("CGEvent creation failed")
	}
	if down {
		n.clickCounts[b] = n.clicks.press(b, p.X, p.Y, time.Now())
	}
	count := n.clickCounts[b]
	if count == 0 {
		count = 1
	}
	setIntegerValue(e, 1, count) // kCGMouseEventClickState
	n.buttons[b] = down
	return n.emit(e)
}
func (n *native) Wheel(x, y float64) error { return n.emit(scroll(nil, 0, 2, int32(-y), int32(-x), 0)) }
func (n *native) Key(code string, down bool) error {
	k, ok := macCodes[code]
	if !ok {
		return fmt.Errorf("unsupported macOS key %q", code)
	}
	previous := n.modifiers
	n.modifiers.update(code, down)
	if err := n.emit(createKey(nil, k, down)); err != nil {
		n.modifiers = previous
		return err
	}
	return nil
}
func (*native) Close() error { return nil }
