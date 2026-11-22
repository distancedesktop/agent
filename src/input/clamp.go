package input

type rect struct{ X, Y, W, H float64 }

// clampToRects returns p unchanged when it lies on a display, otherwise the
// nearest point that does. A pointer posted outside every display stays there
// on macOS, so later relative moves would have to undo the overshoot first.
func clampToRects(x, y float64, rects []rect) (float64, float64) {
	bestX, bestY, best := x, y, -1.0
	for _, r := range rects {
		if r.W < 1 || r.H < 1 {
			continue
		}
		cx := min(max(x, r.X), r.X+r.W-1)
		cy := min(max(y, r.Y), r.Y+r.H-1)
		d := (cx-x)*(cx-x) + (cy-y)*(cy-y)
		if best < 0 || d < best {
			bestX, bestY, best = cx, cy, d
		}
	}
	return bestX, bestY
}
