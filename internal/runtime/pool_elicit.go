package runtime

import (
	"context"
	"sync"
	"time"

	"github.com/dedene/mcparcel/internal/elicit"
	"github.com/dedene/mcparcel/internal/mcpclient"
)

// callDeadline is a call deadline that stops counting while a prompt waits
// for the user. Err is context.DeadlineExceeded once it fires. Value is the
// parent's, so context.Cause(d) is the parent's cause: nil after the timer
// fired, errForced after a forced stop.
type callDeadline struct {
	context.Context
	done      chan struct{}
	mu        sync.Mutex
	err       error
	timer     *time.Timer
	end       time.Time
	remaining time.Duration
	paused    int
}

func withCallDeadline(parent context.Context, d time.Duration) (*callDeadline, context.CancelFunc) {
	c := &callDeadline{Context: parent, done: make(chan struct{})}
	c.mu.Lock()
	c.end = time.Now().Add(d)
	c.timer = time.AfterFunc(d, func() { c.finish(context.DeadlineExceeded) })
	c.mu.Unlock()
	stop := context.AfterFunc(parent, func() { c.finish(parent.Err()) })
	return c, func() { stop(); c.finish(context.Canceled) }
}

func (c *callDeadline) Done() <-chan struct{}       { return c.done }
func (c *callDeadline) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *callDeadline) Err() error                  { c.mu.Lock(); defer c.mu.Unlock(); return c.err }

func (c *callDeadline) finish(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		c.err = err
		c.timer.Stop()
		close(c.done)
	}
}

// pause stops the clock until the returned resume runs; pauses nest.
func (c *callDeadline) pause() (resume func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paused++; c.paused == 1 && c.err == nil && c.timer.Stop() {
		c.remaining = time.Until(c.end)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.paused--; c.paused == 0 && c.err == nil {
				c.end = time.Now().Add(c.remaining)
				c.timer.Reset(c.remaining)
			}
		})
	}
}

// forwardPrompts wraps the caller's prompter so a prompt ends with the call,
// pauses the call deadline while it is open and logs its outcome.
func (p *pool) forwardPrompts(callCtx context.Context, d *callDeadline) context.Context {
	outer := mcpclient.PrompterFrom(callCtx)
	if outer == nil {
		return callCtx
	}
	return mcpclient.WithPrompter(callCtx, &mcpclient.Prompter{Forms: outer.Forms, Ask: func(ctx context.Context, prompt elicit.Prompt) elicit.Answer {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		defer context.AfterFunc(callCtx, cancel)()
		defer d.pause()()
		p.opts.Log("elicitation_forwarded")
		a := outer.Ask(ctx, prompt)
		if prompt.Check(a) != nil {
			a = elicit.Answer{Action: "cancel"}
		}
		p.opts.Log(map[string]string{"accept": "elicitation_accepted", "decline": "elicitation_declined", "cancel": "elicitation_canceled"}[a.Action])
		return a
	}})
}
