//go:build darwin

package input

import (
	"testing"
	"unsafe"
)

func TestMouseClickState(t *testing.T) {
	oldFlags := setFlags
	setFlags = func(unsafe.Pointer, uint64) {}
	defer func() { setFlags = oldFlags }()
	oldCreate, oldLocation, oldGet, oldSet, oldPost, oldRelease := createMouse, createEvent, getLocation, setIntegerValue, post, release
	defer func() {
		createMouse, createEvent, getLocation, setIntegerValue, post, release = oldCreate, oldLocation, oldGet, oldSet, oldPost, oldRelease
	}()
	p := unsafe.Pointer(new(byte))
	createMouse = func(unsafe.Pointer, uint32, point, uint32) unsafe.Pointer { return p }
	createEvent = func(unsafe.Pointer) unsafe.Pointer { return p }
	getLocation = func(unsafe.Pointer) point { return point{10, 20} }
	var counts []int64
	setIntegerValue = func(_ unsafe.Pointer, field uint32, count int64) {
		if field != 1 {
			t.Fatalf("unexpected field %d", field)
		}
		counts = append(counts, count)
	}
	post = func(uint32, unsafe.Pointer) {}
	release = func(unsafe.Pointer) {}
	n := &native{}
	for _, down := range []bool{true, false, true, false} {
		if err := n.Button(0, down); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range []int64{1, 1, 2, 2} {
		if counts[i] != want {
			t.Fatalf("event %d click state = %d, want %d", i, counts[i], want)
		}
	}
	if err := n.Button(5, true); err == nil {
		t.Fatal("accepted invalid button")
	}
	createMouse = func(unsafe.Pointer, uint32, point, uint32) unsafe.Pointer { return nil }
	if err := n.Button(0, true); err == nil {
		t.Fatal("accepted failed event creation")
	}
}
