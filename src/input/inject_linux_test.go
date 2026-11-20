//go:build linux

package input

import (
	"testing"
	"unsafe"
)

func TestX11PhysicalKeys(t *testing.T) {
	oldKey, oldFlush := key, flush
	defer func() { key, flush = oldKey, oldFlush }()
	var gotCode uint32
	var gotDown int32
	key = func(_ unsafe.Pointer, code uint32, down int32, _ uintptr) int32 {
		gotCode, gotDown = code, down
		return 1
	}
	flush = func(unsafe.Pointer) int32 { return 1 }
	n := &native{}
	for code, want := range map[string]uint32{"Slash": 61, "KeyY": 29, "KeyZ": 52, "ControlRight": 105, "ArrowUp": 111, "F24": 202} {
		for _, down := range []bool{true, false} {
			if err := n.Key(code, down); err != nil {
				t.Fatal(err)
			}
			if gotCode != want || gotDown != truth(down) {
				t.Errorf("%s down=%v: got %d/%d, want %d/%d", code, down, gotCode, gotDown, want, truth(down))
			}
		}
	}
	if err := n.Key("", true); err == nil {
		t.Fatal("accepted empty code")
	}
	if err := n.Key("bogus", true); err == nil {
		t.Fatal("accepted unknown code")
	}
}
