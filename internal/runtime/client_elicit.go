package runtime

import (
	"context"

	"github.com/dedene/mcparcel/internal/elicit"
)

// prompts runs a call's prompts one at a time; answers holds the open
// prompt's answer for the exchange to write.
type prompts struct {
	ctx     context.Context
	ask     func(context.Context, elicit.Prompt) elicit.Answer
	answers chan ElicitAnswer
	stop    func()
}

func newPrompts(ctx context.Context, ask func(context.Context, elicit.Prompt) elicit.Answer) *prompts {
	return &prompts{ctx: ctx, ask: ask, answers: make(chan ElicitAnswer, 1), stop: func() {}}
}

// start closes the open prompt, dropping its answer, and opens e. The daemon
// sends a new prompt only after it resolved the previous one.
func (p *prompts) start(e Elicit) {
	p.stop()
	select {
	case <-p.answers:
	default:
	}
	ctx, cancel := context.WithTimeout(p.ctx, elicit.PromptTimeout)
	done := make(chan struct{})
	go func() {
		defer close(done)
		a := p.ask(ctx, e.Prompt)
		if a.Valid() != nil {
			a = elicit.Answer{Action: "cancel"}
		}
		select {
		case p.answers <- ElicitAnswer{PromptID: e.PromptID, Answer: a}:
		default:
		}
	}()
	p.stop = func() { cancel(); <-done }
}
