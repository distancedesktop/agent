package input

import (
	"fmt"
	"math"
)

type Injector interface {
	Mouse(dx, dy int) error
	Move(x, y int) error
	Button(button int, down bool) error
	Wheel(dx, dy float64) error
	Key(code string, down bool) error
	Close() error
}

type Event struct {
	Kind    string  `json:"kind"`
	DX      float64 `json:"dx"`
	DY      float64 `json:"dy"`
	Buttons int     `json:"buttons"`
	Button  int     `json:"button"`
	Code    string  `json:"code"`
	Down    bool    `json:"down"`
	ID      int     `json:"id"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Phase   string  `json:"phase"`
}

type Geometry struct{ X, Y, Width, Height int }

type Controller struct {
	Injector
	keys    map[string]bool
	buttons map[int]bool
	touch   *int
}

func NewController(i Injector) *Controller {
	return &Controller{Injector: i, keys: map[string]bool{}, buttons: map[int]bool{}}
}

func (c *Controller) button(b int, down bool) error {
	if b < 0 || b > 4 {
		return fmt.Errorf("unsupported mouse button %d", b)
	}
	if err := c.Button(b, down); err != nil {
		return err
	}
	c.buttons[b] = down
	return nil
}

func (c *Controller) Handle(e Event, g Geometry) error {
	for _, v := range []float64{e.DX, e.DY, e.X, e.Y} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("non-finite input coordinate")
		}
	}
	switch e.Kind {
	case "mouse":
		if math.Abs(e.DX) > 32767 || math.Abs(e.DY) > 32767 {
			return fmt.Errorf("mouse delta out of range")
		}
		return c.Mouse(int(e.DX), int(e.DY))
	case "mousedown", "mouseup":
		return c.button(e.Button, e.Kind == "mousedown")
	case "wheel":
		if math.Abs(e.DX) > 10000 || math.Abs(e.DY) > 10000 {
			return fmt.Errorf("wheel delta out of range")
		}
		return c.Wheel(e.DX, e.DY)
	case "key":
		if _, ok := Keysym(e.Code); !ok {
			return fmt.Errorf("unsupported key code %q", e.Code)
		}
		if err := c.Key(e.Code, e.Down); err != nil {
			return err
		}
		c.keys[e.Code] = e.Down
		return nil
	case "touch":
		if e.Phase != "start" && e.Phase != "move" && e.Phase != "end" && e.Phase != "cancel" {
			return fmt.Errorf("unknown touch phase %q", e.Phase)
		}
		if c.touch != nil && *c.touch != e.ID {
			return nil
		}
		if e.Phase == "start" && c.touch == nil {
			id := e.ID
			c.touch = &id
		}
		if c.touch == nil {
			return nil
		}
		if g.Width <= 0 || g.Height <= 0 {
			return fmt.Errorf("active display geometry unavailable")
		}
		x := g.X + int(math.Max(0, math.Min(1, e.X))*float64(g.Width-1))
		y := g.Y + int(math.Max(0, math.Min(1, e.Y))*float64(g.Height-1))
		if err := c.Move(x, y); err != nil {
			return err
		}
		if e.Phase == "end" || e.Phase == "cancel" {
			c.touch = nil
			return c.button(0, false)
		}
		if e.Phase == "start" {
			return c.button(0, true)
		}
		return nil
	}
	return fmt.Errorf("unknown input kind %q", e.Kind)
}

func (c *Controller) Close() error {
	for k, down := range c.keys {
		if down {
			c.Key(k, false)
		}
	}
	for b, down := range c.buttons {
		if down {
			c.Button(b, false)
		}
	}
	return c.Injector.Close()
}

var keysyms = map[string]uint32{
	"Enter": 0xff0d, "Escape": 0xff1b, "Backspace": 0xff08, "Tab": 0xff09, "Space": 0x20,
	"ShiftLeft": 0xffe1, "ShiftRight": 0xffe2, "ControlLeft": 0xffe3, "ControlRight": 0xffe4, "AltLeft": 0xffe9, "AltRight": 0xffea, "MetaLeft": 0xffeb, "MetaRight": 0xffec,
	"CapsLock": 0xffe5, "NumLock": 0xff7f, "ScrollLock": 0xff14, "Pause": 0xff13, "PrintScreen": 0xff61, "ContextMenu": 0xff67,
	"ArrowLeft": 0xff51, "ArrowUp": 0xff52, "ArrowRight": 0xff53, "ArrowDown": 0xff54, "Home": 0xff50, "End": 0xff57, "PageUp": 0xff55, "PageDown": 0xff56, "Insert": 0xff63, "Delete": 0xffff,
	"Minus": '-', "Equal": '=', "BracketLeft": '[', "BracketRight": ']', "Backslash": '\\', "Semicolon": ';', "Quote": '\'', "Backquote": '`', "Comma": ',', "Period": '.', "Slash": '/', "IntlBackslash": '<',
	"NumpadAdd": 0xffab, "NumpadSubtract": 0xffad, "NumpadMultiply": 0xffaa, "NumpadDivide": 0xffaf, "NumpadDecimal": 0xffae, "NumpadEnter": 0xff8d, "NumpadEqual": 0xffbd,
}

func Keysym(code string) (uint32, bool) {
	if len(code) == 4 && code[:3] == "Key" && code[3] >= 'A' && code[3] <= 'Z' {
		return uint32(code[3] + 32), true
	}
	if len(code) == 6 && code[:5] == "Digit" && code[5] >= '0' && code[5] <= '9' {
		return uint32(code[5]), true
	}
	if len(code) == 7 && code[:6] == "Numpad" && code[6] >= '0' && code[6] <= '9' {
		return 0xffb0 + uint32(code[6]-'0'), true
	}
	for i := 1; i <= 24; i++ {
		if code == fmt.Sprintf("F%d", i) {
			return 0xffbd + uint32(i), true
		}
	}
	v, ok := keysyms[code]
	return v, ok
}
