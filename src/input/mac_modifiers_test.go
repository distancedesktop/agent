package input

import (
	"fmt"
	"strings"
	"testing"
)

func TestMacModifierFlags(t *testing.T) {
	var m macModifiers
	if m.flags() != 0 {
		t.Fatal("modifiers initially set")
	}
	for pair := 0; pair < 4; pair++ {
		left, right := macModifierCodes[pair*2], macModifierCodes[pair*2+1]
		want := uint64(1 << (17 + pair))
		m.update(left, true)
		m.update(right, true)
		m.update(left, false)
		if m.flags() != want {
			t.Fatalf("%s release cleared %s", left, right)
		}
		m.update(right, false)
		if m.flags() != 0 {
			t.Fatal("modifier stuck")
		}
	}
	m.update("MetaLeft", true)
	m.update("ShiftRight", true)
	m.update("KeyC", true)
	if m.flags() != 1<<20|1<<17 {
		t.Fatal("chord flags missing")
	}
	m.update("CapsLock", true)
	m.update("CapsLock", true)
	m.update("CapsLock", false)
	if m.flags()&(1<<16) == 0 {
		t.Fatal("caps repeat toggled twice")
	}
	m.update("CapsLock", true)
	m.update("CapsLock", false)
	if m.flags()&(1<<16) != 0 {
		t.Fatal("caps did not toggle off")
	}
}

func TestMacKeyCoverageDocumentedGaps(t *testing.T) {
	gaps := map[string]bool{"PrintScreen": true, "Pause": true, "ScrollLock": true, "NumLock": true, "Insert": true, "ContextMenu": true, "IntlBackslash": true}
	for _, code := range acceptedCodes() {
		_, mapped := macCodes[code]
		gap := gaps[code] || strings.HasPrefix(code, "Numpad")
		for i := 13; i <= 24; i++ {
			if code == fmt.Sprintf("F%d", i) {
				gap = true
			}
		}
		if mapped == gap {
			t.Errorf("%s: mapping=%v documented gap=%v", code, mapped, gap)
		}
	}
}
