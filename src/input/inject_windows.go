//go:build windows

package input

import (
	"encoding/binary"
	"fmt"
	"syscall"
	"unsafe"
)

var user32 = syscall.NewLazyDLL("user32.dll")
var sendInput = user32.NewProc("SendInput")
var getMetric = user32.NewProc("GetSystemMetrics")
var getCursorPos = user32.NewProc("GetCursorPos")
var setDPIContext = user32.NewProc("SetProcessDpiAwarenessContext")
var setDPIAware = user32.NewProc("SetProcessDPIAware")

type native struct{ wheel wheelAccumulator }

func New() (Injector, error) {
	if err := sendInput.Find(); err != nil {
		return nil, err
	}
	// Cursor and desktop coordinates must be physical pixels, like the captured
	// geometry; a DPI-unaware process gets them scaled on a scaled display.
	if setDPIContext.Find() != nil {
		setDPIAware.Call()
	} else if ok, _, _ := setDPIContext.Call(^uintptr(3)); ok == 0 { // PER_MONITOR_AWARE_V2
		setDPIAware.Call()
	}
	return &native{}, nil
}
func winSend(kind uint32, fill func([]byte)) error {
	size, off := 40, 8
	if unsafe.Sizeof(uintptr(0)) == 4 {
		size, off = 28, 4
	}
	b := make([]byte, size)
	binary.LittleEndian.PutUint32(b, kind)
	fill(b[off:])
	n, _, err := sendInput.Call(1, uintptr(unsafe.Pointer(&b[0])), uintptr(size))
	if n != 1 {
		return fmt.Errorf("SendInput: %v", err)
	}
	return nil
}
func winMouse(x, y int, data, flags uint32) error {
	return winSend(0, func(b []byte) {
		binary.LittleEndian.PutUint32(b, uint32(x))
		binary.LittleEndian.PutUint32(b[4:], uint32(y))
		binary.LittleEndian.PutUint32(b[8:], data)
		binary.LittleEndian.PutUint32(b[12:], flags)
	})
}
// Mouse moves the pointer by exactly (dx, dy) pixels. A relative SendInput move
// goes through the host pointer speed and acceleration settings, so the move is
// sent as an absolute one from the current position instead.
func (n *native) Mouse(dx, dy int) error {
	var p struct{ X, Y int32 }
	if ok, _, _ := getCursorPos.Call(uintptr(unsafe.Pointer(&p))); ok == 0 {
		return winMouse(dx, dy, 0, 1)
	}
	return n.Move(int(p.X)+dx, int(p.Y)+dy)
}
func metric(id uintptr) int          { v, _, _ := getMetric.Call(id); return int(int32(v)) }
func (*native) Move(x, y int) error {
	w, h := metric(78), metric(79)
	if w < 2 || h < 2 {
		return fmt.Errorf("desktop geometry unavailable")
	}
	return winMouse(windowsAbsolute(x-metric(76), w), windowsAbsolute(y-metric(77), h), 0, 0xc001)
}
func (*native) Button(b int, down bool) error {
	if b < 0 || b > 4 {
		return fmt.Errorf("invalid button")
	}
	flags := [][2]uint32{{4, 2}, {64, 32}, {16, 8}, {256, 128}, {256, 128}}
	data := uint32(0)
	if b >= 3 {
		data = uint32(b - 2)
	}
	i := 0
	if down {
		i = 1
	}
	return winMouse(0, 0, data, flags[b][i])
}
func (n *native) Wheel(x, y float64) error {
	dx, dy := n.wheel.add(x*3, -y*3)
	if dx != 0 {
		if err := winMouse(0, 0, uint32(dx), 0x1000); err != nil {
			return err
		}
	}
	if dy != 0 {
		return winMouse(0, 0, uint32(dy), 0x800)
	}
	return nil
}
func (*native) Key(code string, down bool) error {
	vk, scan, flags, err := windowsKeyFields(code, down)
	if err != nil {
		return err
	}
	return winSend(1, func(b []byte) {
		binary.LittleEndian.PutUint16(b, vk)
		binary.LittleEndian.PutUint16(b[2:], scan)
		binary.LittleEndian.PutUint32(b[4:], flags)
	})
}
func (*native) Close() error { return nil }
