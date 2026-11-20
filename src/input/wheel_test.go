package input

import "testing"

func TestWheelAccumulator(t *testing.T) {
	var w wheelAccumulator
	for i, tc := range []struct {
		x, y         float64
		wantX, wantY int32
	}{
		{0, 0, 0, 0},
		{0.25, -0.125, 0, 0},
		{0.25, -0.125, 0, 0},
		{0.25, -0.125, 0, 0},
		{0.25, -0.125, 1, 0},
		{0, -0.5, 0, -1},
		{-0.75, 0.75, 0, 0},
		{0.5, -0.5, 0, 0},
		{-0.75, 0.75, -1, 1},
		{2.5, -3.5, 2, -3},
		{0.5, -0.5, 1, -1},
		{0, 0, 0, 0},
	} {
		x, y := w.add(tc.x, tc.y)
		if x != tc.wantX || y != tc.wantY {
			t.Errorf("step %d: (%d,%d), want (%d,%d)", i, x, y, tc.wantX, tc.wantY)
		}
	}
}
