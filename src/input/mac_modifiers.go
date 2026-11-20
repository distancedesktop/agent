package input

type macModifiers struct {
	held           [8]bool
	caps, capsDown bool
}

var macModifierCodes = [8]string{"ShiftLeft", "ShiftRight", "ControlLeft", "ControlRight", "AltLeft", "AltRight", "MetaLeft", "MetaRight"}

func (m *macModifiers) update(code string, down bool) {
	for i, modifier := range macModifierCodes {
		if modifier == code {
			m.held[i] = down
			return
		}
	}
	if code == "CapsLock" {
		if down && !m.capsDown {
			m.caps = !m.caps
		}
		m.capsDown = down
	}
}

func (m *macModifiers) flags() uint64 {
	var flags uint64
	if m.caps {
		flags |= 1 << 16
	}
	for pair := 0; pair < 4; pair++ {
		if m.held[pair*2] || m.held[pair*2+1] {
			flags |= 1 << (17 + pair)
		}
	}
	return flags
}
