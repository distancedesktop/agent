package input

import (
	"testing"
	"time"
)

func TestClickTracker(t *testing.T) {
	start := time.Unix(1000, 0)
	var c clickTracker
	for _, tc := range []struct {
		name   string
		button int
		x, y   float64
		ms     int
		want   int64
	}{
		{"first", 0, 10, 20, 0, 1},
		{"double", 0, 11, 21, 100, 2},
		{"triple at interval boundary", 0, 11, 21, 600, 3},
		{"timeout", 0, 11, 21, 1101, 1},
		{"different button", 2, 11, 21, 1200, 1},
		{"right double", 2, 11, 21, 1300, 2},
		{"moved too far", 2, 16, 21, 1400, 1},
		{"distance boundary", 2, 20, 21, 1500, 2},
		{"left after right", 0, 20, 21, 1600, 1},
		{"backwards time", 0, 20, 21, 1599, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.press(tc.button, tc.x, tc.y, start.Add(time.Duration(tc.ms)*time.Millisecond)); got != tc.want {
				t.Fatalf("count = %d, want %d", got, tc.want)
			}
		})
	}
}
