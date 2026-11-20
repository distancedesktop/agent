package input

import "time"

type clickTracker struct {
	button int
	x, y   float64
	at     time.Time
	count  int64
}

func (c *clickTracker) press(button int, x, y float64, now time.Time) int64 {
	dx, dy := x-c.x, y-c.y
	elapsed := now.Sub(c.at)
	if c.count > 0 && button == c.button && elapsed >= 0 && elapsed <= 500*time.Millisecond && dx*dx+dy*dy <= 16 {
		c.count++
	} else {
		c.count = 1
	}
	c.button, c.x, c.y, c.at = button, x, y, now
	return c.count
}
