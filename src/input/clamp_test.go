package input

import "testing"

func TestClampToRects(t *testing.T) {
	displays := []rect{{0, 0, 1600, 1200}, {-800, 100, 800, 600}}
	for _, c := range []struct{ x, y, wantX, wantY float64 }{
		{700, 600, 700, 600},
		{-400, 300, -400, 300},
		{-5060, -4663, -800, 100},
		{500, -4663, 500, 0},
		{8940, 9337, 1599, 1199},
		{-900, 300, -800, 300},
		{-400, 50, -400, 100},
		{-10, 900, 0, 900},
	} {
		if x, y := clampToRects(c.x, c.y, displays); x != c.wantX || y != c.wantY {
			t.Errorf("clamp(%v,%v) = %v,%v, want %v,%v", c.x, c.y, x, y, c.wantX, c.wantY)
		}
	}
	if x, y := clampToRects(-5, 7, nil); x != -5 || y != 7 {
		t.Errorf("no displays: got %v,%v", x, y)
	}
}
