package auth

import (
	"testing"
	"time"
	"unsafe"
)

// Shift only the wall seconds in Go's monotonic time representation. Time.Add
// shifts both clocks and cannot model a system clock correction.
func shiftWall(t time.Time, seconds int64) time.Time {
	wall := (*uint64)(unsafe.Pointer(&t))
	if *wall>>63 != 1 {
		panic("test requires monotonic time")
	}
	*wall += uint64(seconds) << 30
	return t
}

func TestExpiryWithIndependentWallClock(t *testing.T) {
	for _, seconds := range []int64{-60, 7200} {
		t.Run(time.Duration(seconds*time.Second.Nanoseconds()).String(), func(t *testing.T) {
			now := time.Now()
			s := profileState{client: localSecretClient{}, started: now, lastNow: now, deadline: now.Add(time.Hour)}
			changed := shiftWall(now, seconds)
			if changed.Sub(now) != 0 {
				t.Fatal("test changed monotonic clock")
			}
			s.checkExpiry(changed)
			if !s.expired || s.client != nil {
				t.Fatal("wall correction did not expire session")
			}
		})
	}
}
