//go:build darwin

package input

import (
	"fmt"
	"sync"
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

type point struct{ X, Y float64 }
type native struct{ buttons [5]bool }

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
	})
	if macErr != nil {
		return nil, macErr
	}
	if !trusted() {
		return nil, fmt.Errorf("input unavailable: grant Accessibility permission to the agent")
	}
	return &native{}, nil
}
func emit(e unsafe.Pointer) error {
	if e == nil {
		return fmt.Errorf("CGEvent creation failed")
	}
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
	return emit(createMouse(nil, kind, point{float64(x), float64(y)}, b))
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
	n.buttons[b] = down
	return emit(createMouse(nil, kind, p, cb))
}
func (*native) Wheel(x, y float64) error { return emit(scroll(nil, 0, 2, int32(-y), int32(-x), 0)) }
func (*native) Key(code string, down bool) error {
	k, ok := macCodes[code]
	if !ok {
		return fmt.Errorf("unsupported macOS key %q", code)
	}
	return emit(createKey(nil, k, down))
}
func (*native) Close() error { return nil }

var macCodes = map[string]uint16{
	"KeyA": 0, "KeyS": 1, "KeyD": 2, "KeyF": 3, "KeyH": 4, "KeyG": 5, "KeyZ": 6, "KeyX": 7, "KeyC": 8, "KeyV": 9, "KeyB": 11, "KeyQ": 12, "KeyW": 13, "KeyE": 14, "KeyR": 15, "KeyY": 16, "KeyT": 17, "Digit1": 18, "Digit2": 19, "Digit3": 20, "Digit4": 21, "Digit6": 22, "Digit5": 23, "Equal": 24, "Digit9": 25, "Digit7": 26, "Minus": 27, "Digit8": 28, "Digit0": 29, "BracketRight": 30, "KeyO": 31, "KeyU": 32, "BracketLeft": 33, "KeyI": 34, "KeyP": 35, "Enter": 36, "KeyL": 37, "KeyJ": 38, "Quote": 39, "KeyK": 40, "Semicolon": 41, "Backslash": 42, "Comma": 43, "Slash": 44, "KeyN": 45, "KeyM": 46, "Period": 47, "Tab": 48, "Space": 49, "Backquote": 50, "Backspace": 51, "Escape": 53, "MetaRight": 54, "MetaLeft": 55, "ShiftLeft": 56, "CapsLock": 57, "AltLeft": 58, "ControlLeft": 59, "ShiftRight": 60, "AltRight": 61, "ControlRight": 62,
	"F1": 122, "F2": 120, "F3": 99, "F4": 118, "F5": 96, "F6": 97, "F7": 98, "F8": 100, "F9": 101, "F10": 109, "F11": 103, "F12": 111, "Home": 115, "PageUp": 116, "Delete": 117, "End": 119, "PageDown": 121, "ArrowLeft": 123, "ArrowRight": 124, "ArrowDown": 125, "ArrowUp": 126,
}
