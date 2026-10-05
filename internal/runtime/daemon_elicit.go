package runtime

import (
	"context"
	"sync"
	"time"

	"github.com/dedene/mcparcel/internal/elicit"
)

// socketPrompter forwards a call's elicitations to its CLI, one at a time,
// and keeps only the answer to the prompt that is open.
type socketPrompter struct {
	life    context.Context
	send    func(context.Context, Elicit) error
	timeout time.Duration
	turn    chan struct{}
	mu      sync.Mutex
	open    string
	answer  chan elicit.Answer
}

func newSocketPrompter(life context.Context, send func(context.Context, Elicit) error, timeout time.Duration) *socketPrompter {
	s := &socketPrompter{life: life, send: send, timeout: timeout, turn: make(chan struct{}, 1)}
	s.turn <- struct{}{}
	return s
}

// ask sends p and waits for its answer; the end of ctx or of the socket, the
// timeout or a failed send answers cancel.
func (s *socketPrompter) ask(ctx context.Context, p elicit.Prompt) elicit.Answer {
	canceled := elicit.Answer{Action: "cancel"}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(s.life, cancel)()
	select {
	case <-ctx.Done():
		return canceled
	case <-s.turn:
	}
	defer func() { s.turn <- struct{}{} }()
	id := newRequestID()
	if id == "" {
		return canceled
	}
	answer := make(chan elicit.Answer, 1)
	s.mu.Lock()
	s.open, s.answer = id, answer
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.open, s.answer = "", nil; s.mu.Unlock() }()
	timer := time.NewTimer(s.timeout)
	defer timer.Stop()
	if s.send(ctx, Elicit{PromptID: id, Prompt: p}) != nil {
		return canceled
	}
	select {
	case a := <-answer:
		return a
	case <-ctx.Done():
	case <-timer.C:
	}
	return canceled
}

// deliver hands a the open prompt; a stale or duplicate answer is dropped.
func (s *socketPrompter) deliver(a ElicitAnswer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open == "" || a.PromptID != s.open {
		return
	}
	s.open = ""
	s.answer <- a.Answer
}
