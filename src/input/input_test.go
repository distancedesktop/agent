package input

import "testing"

func TestEvdevCode(t *testing.T) {
	for code, want := range map[string]uint16{
		"KeyA": 30, "KeyZ": 44, "KeyY": 21, "Digit0": 11,
		"ShiftLeft": 42, "ShiftRight": 54, "ControlLeft": 29, "ControlRight": 97,
		"AltRight": 100, "MetaLeft": 125, "MetaRight": 126, "ContextMenu": 127,
		"Enter": 28, "ArrowUp": 103, "ArrowLeft": 105, "ArrowRight": 106,
		"ArrowDown": 108, "Home": 102, "End": 107, "PageUp": 104,
		"PageDown": 109, "Insert": 110, "Delete": 111,
		"F5": 63, "F13": 183, "F24": 194, "Numpad3": 81,
		"NumpadEnter": 96, "NumpadDivide": 98, "NumpadEqual": 117,
		"Slash": 53, "IntlBackslash": 86, "PrintScreen": 99, "Pause": 119,
	} {
		got, ok := EvdevCode(code)
		if !ok || got != want {
			t.Errorf("%s = %#x, %v; want %#x", code, got, ok, want)
		}
	}
	for _, code := range []string{"", "Keya", "F0", "F25", "DigitX", "bogus"} {
		if _, ok := EvdevCode(code); ok {
			t.Errorf("accepted %q", code)
		}
	}
}

type fake struct {
	moves, buttons, keys int
	x, y                 int
}

func (f *fake) Mouse(x, y int) error         { f.moves++; f.x = x; f.y = y; return nil }
func (f *fake) Move(x, y int) error          { return f.Mouse(x, y) }
func (f *fake) Button(int, bool) error       { f.buttons++; return nil }
func (f *fake) Wheel(float64, float64) error { return nil }
func (f *fake) Key(string, bool) error       { f.keys++; return nil }
func (f *fake) Close() error                 { return nil }
func TestController(t *testing.T) {
	f := &fake{}
	c := NewController(f)
	g := Geometry{X: 100, Y: 200, Width: 101, Height: 51}
	for _, e := range []Event{{Kind: "mouse", DX: 3, DY: -2}, {Kind: "key", Code: "KeyA", Down: true}, {Kind: "touch", ID: 1, X: 0.5, Y: 1, Phase: "start"}, {Kind: "touch", ID: 2, Phase: "start"}, {Kind: "touch", ID: 1, Phase: "end"}} {
		if err := c.Handle(e, g); err != nil {
			t.Fatal(err)
		}
	}
	if f.moves != 3 || f.buttons != 2 {
		t.Fatalf("moves=%d buttons=%d", f.moves, f.buttons)
	}
	for _, e := range []Event{{}, {Kind: "key"}, {Kind: "mousedown", Button: 9}, {Kind: "mouse", DX: 1e20}, {Kind: "touch", Phase: "bogus"}} {
		if c.Handle(e, g) == nil {
			t.Errorf("accepted %+v", e)
		}
	}
	c.Close()
	if f.keys != 2 {
		t.Fatal("held key not released")
	}
}
