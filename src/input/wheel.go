package input

import "math"

type wheelAccumulator struct {
	x, y float64
}

func (w *wheelAccumulator) add(x, y float64) (int32, int32) {
	wholeX, remainderX := math.Modf(w.x + x)
	wholeY, remainderY := math.Modf(w.y + y)
	w.x, w.y = remainderX, remainderY
	return int32(wholeX), int32(wholeY)
}
