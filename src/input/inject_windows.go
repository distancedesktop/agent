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

type native struct{}

func New() (Injector, error) {
	if err := sendInput.Find(); err != nil {
		return nil, err
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
func (*native) Mouse(x, y int) error { return winMouse(x, y, 0, 1) }
func metric(id uintptr) int          { v, _, _ := getMetric.Call(id); return int(int32(v)) }
func (*native) Move(x, y int) error {
	w, h := metric(78), metric(79)
	if w < 2 || h < 2 {
		return fmt.Errorf("desktop geometry unavailable")
	}
	return winMouse((x-metric(76))*65535/(w-1), (y-metric(77))*65535/(h-1), 0, 0xc001)
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
func (*native) Wheel(x, y float64) error {
	if x != 0 {
		if err := winMouse(0, 0, uint32(int32(x*3)), 0x1000); err != nil {
			return err
		}
	}
	if y != 0 {
		return winMouse(0, 0, uint32(int32(-y*3)), 0x800)
	}
	return nil
}
func (*native) Key(code string, down bool) error {
	scan, ok := scanCodes[code]
	if !ok {
		return fmt.Errorf("unsupported Windows key %q", code)
	}
	flags := uint32(8)
	if scan&0x100 != 0 {
		flags |= 1
	}
	if !down {
		flags |= 2
	}
	return winSend(1, func(b []byte) {
		binary.LittleEndian.PutUint16(b[2:], scan&255)
		binary.LittleEndian.PutUint32(b[4:], flags)
	})
}
func (*native) Close() error { return nil }
