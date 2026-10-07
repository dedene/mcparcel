package testutil

import (
	"testing"
	"time"
)

func TestAdvanceWallKeepsMonotonic(t *testing.T) {
	for _, d := range []time.Duration{10 * time.Minute, -time.Hour, 1500 * time.Millisecond, -1500 * time.Millisecond} {
		c := NewClock()
		before := c.Now()
		c.AdvanceWall(d)
		after := c.Now()
		if after.Sub(before) != 0 {
			t.Fatalf("%v: monotonic moved by %v", d, after.Sub(before))
		}
		if got := after.Round(0).Sub(before.Round(0)); got != d {
			t.Fatalf("%v: wall moved by %v", d, got)
		}
	}
}
