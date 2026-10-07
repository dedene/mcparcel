//go:build darwin

package spike

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunWithTimeoutReturnsResult(t *testing.T) {
	want := errors.New("probe failed")
	for _, result := range []error{nil, want} {
		err := runWithTimeout(context.Background(), time.Second, func(context.Context) error {
			return result
		})
		if !errors.Is(err, result) {
			t.Fatalf("error = %v, want %v", err, result)
		}
	}
}

func TestRunWithTimeoutExpires(t *testing.T) {
	release := make(chan struct{})
	finished := make(chan struct{})
	defer func() {
		close(release)
		<-finished
	}()
	start := time.Now()
	err := runWithTimeout(context.Background(), 50*time.Millisecond, func(context.Context) error {
		defer close(finished)
		<-release // Model a CGO call that ignores context cancellation.
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "did not answer within 50ms") {
		t.Fatalf("error = %v, want watchdog timeout", err)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("watchdog returned after %s, want under 1 second", elapsed)
	}
}

func TestRunWithTimeoutHonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	defer func() {
		cancel()
		close(release)
		<-finished
	}()
	go func() {
		<-started
		cancel()
	}()
	start := time.Now()
	err := runWithTimeout(ctx, time.Minute, func(context.Context) error {
		defer close(finished)
		close(started)
		<-release
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "did not answer within 1m0s") {
		t.Fatalf("error = %v, want watchdog cancellation", err)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("cancelled probe returned after %s, want under 1 second", elapsed)
	}
}

func TestRedactRemovesTokenFromErrorText(t *testing.T) {
	err := errors.New("request with ops_secret-token-value was rejected")
	if got := redact(err, "ops_secret-token-value"); got != "request with [redacted] was rejected" {
		t.Fatalf("redact = %q", got)
	}
	if got := redact(err, ""); got != err.Error() {
		t.Fatalf("redact with empty token = %q, want the error unchanged", got)
	}
}
