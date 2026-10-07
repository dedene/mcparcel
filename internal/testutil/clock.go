package testutil

import (
	"sync"
	"time"
	"unsafe"
)

// Clock is a settable fake clock. It starts at the real time, monotonic
// reading included, and only moves when Advance or AdvanceWall is called.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

func NewClock() *Clock { return &Clock{t: time.Now()} }

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance moves the clock by d; a negative d moves it back.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// AdvanceWall moves only the wall reading by d and keeps the monotonic one, as
// a Mac sleep (monotonic stopped) or a system clock correction does. Time.Add
// cannot model this: it moves both readings together.
func (c *Clock) AdvanceWall(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Go stores a monotonic time's wall reading as 1 flag bit, 33 bits of
	// seconds since 1885 and 30 bits of nanoseconds.
	wall := (*uint64)(unsafe.Pointer(&c.t))
	if *wall>>63 != 1 {
		panic("testutil: AdvanceWall needs a monotonic time")
	}
	sec := int64(*wall>>30&(1<<33-1)) + int64(d/time.Second)
	nsec := int64(*wall&(1<<30-1)) + int64(d%time.Second)
	if nsec < 0 {
		nsec += int64(time.Second)
		sec--
	} else if nsec >= int64(time.Second) {
		nsec -= int64(time.Second)
		sec++
	}
	*wall = 1<<63 | uint64(sec)<<30 | uint64(nsec)
}
