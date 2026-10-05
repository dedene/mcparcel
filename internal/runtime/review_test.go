package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/mcpclient"
	"github.com/dedene/mcparcel/internal/output"
)

type discoveryFailureSession struct{ closed bool }

func (s *discoveryFailureSession) Tools(context.Context) ([]json.RawMessage, error) {
	return nil, errors.New("dead session")
}

func (s *discoveryFailureSession) Call(context.Context, string, map[string]any, func() error) (mcpclient.Result, error) {
	panic("unexpected call")
}
func (s *discoveryFailureSession) Close(context.Context) error { s.closed = true; return nil }

func TestDiscoveryFailureRetiresSession(t *testing.T) {
	r := newRig(t)
	r.stdio("a", false)
	first := &discoveryFailureSession{}
	connect := r.opts.Connect
	n := 0
	r.opts.Connect = func(ctx context.Context, o mcpclient.ConnectOptions) (mcpclient.Session, error) {
		n++
		if n == 1 {
			return first, nil
		}
		return connect(ctx, o)
	}
	r.start()
	if res := r.call(testCtx(t), "a", "counter"); res.Error == nil {
		t.Fatal("discovery succeeded")
	}
	if !first.closed {
		t.Fatal("failed discovery session retained")
	}
	count(t, r.call(testCtx(t), "a", "counter"))
	if n != 2 {
		t.Fatal("did not reconnect on next invocation")
	}
}

type shutdownBarrierSession struct {
	discoveryFailureSession
	entered chan<- struct{}
	release <-chan struct{}
}

func (s *shutdownBarrierSession) Tools(context.Context) ([]json.RawMessage, error) {
	return []json.RawMessage{}, nil
}

func (s *shutdownBarrierSession) Close(ctx context.Context) error {
	s.entered <- struct{}{}
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestShutdownClosesSessionsTogether(t *testing.T) {
	r := newRig(t)
	r.stdio("a", false)
	r.stdio("b", false)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	r.opts.Connect = func(context.Context, mcpclient.ConnectOptions) (mcpclient.Session, error) {
		return &shutdownBarrierSession{entered: entered, release: release}, nil
	}
	r.start()
	for _, id := range []string{"a", "b"} {
		success(t, r.h.Handle(testCtx(t), testID, Request{Method: "tools", Connection: id, Arguments: emptyArgs()}, nil))
	}
	done := make(chan error, 1)
	go func() { done <- r.h.Shutdown(testCtx(t), true) }()
	<-entered
	select {
	case <-entered:
	case <-time.After(500 * time.Millisecond):
		close(release)
		<-done
		t.Fatal("session closes were sequential")
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}

func TestAccountConflictErrorCode(t *testing.T) {
	err := poolError(auth.ErrAccountConflict, nil, testID, false)
	if err.Code != "auth_account_conflict" || output.ExitCode(err) != 3 || err.Message != "This daemon already uses another 1Password account; run mcparcel runtime restart." {
		t.Fatal(err)
	}
}
