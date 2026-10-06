package runtime

import (
	"context"
	"testing"
	"time"
)

// TestLargeFrameWriteDeadlineScales: writeSocket's deadline starts after the
// frame is encoded and grows with its size.
func TestLargeFrameWriteDeadlineScales(t *testing.T) {
	c, s := unixPair(t)
	f := largeResponse(t, 48<<20)
	read := make(chan error, 1)
	go func() {
		time.Sleep(2500 * time.Millisecond)
		got, err := readFrame(c, MaxResponseFrameBytes)
		if err == nil && got.Kind != "response" {
			err = ErrInvalidFrame
		}
		read <- err
	}()
	if err := writeSocket(context.Background(), s, makeWriter(), f); err != nil {
		t.Fatal(err)
	}
	if err := <-read; err != nil {
		t.Fatal(err)
	}
	if d := writeDeadline(64); d != 2*time.Second {
		t.Fatal(d)
	}
	if d := writeDeadline(MaxResponseFrameBytes); d != 2*time.Second+16*time.Second {
		t.Fatal(d)
	}
}

// Regression: the daemon's response write was capped at the shutdown timeout
// (5s by default), so a large result never got its size-scaled budget and a
// briefly slow CLI lost it. The cap now applies only once the request is over.
func TestResponseWriteBudget(t *testing.T) {
	buf, err := encodeFrame(largeResponse(t, 48<<20))
	if err != nil {
		t.Fatal(err)
	}
	s := &daemonService{opts: DaemonOptions{ShutdownTimeout: time.Second}}
	t.Run("live request gets the scaled budget", func(t *testing.T) {
		c, conn := unixPair(t)
		read := make(chan error, 1)
		go func() {
			time.Sleep(1500 * time.Millisecond) // past the 1s shutdown timeout
			got, err := readFrame(c, MaxResponseFrameBytes)
			if err == nil && got.Kind != "response" {
				err = ErrInvalidFrame
			}
			read <- err
		}()
		if err := s.writeResponse(context.Background(), conn, makeWriter(), buf); err != nil {
			t.Fatal(err)
		}
		if err := <-read; err != nil {
			t.Fatal(err)
		}
	})
	t.Run("shutdown cancels a live write", func(t *testing.T) {
		_, conn := unixPair(t)
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(200*time.Millisecond, cancel)
		start := time.Now()
		if err := s.writeResponse(ctx, conn, makeWriter(), buf); err == nil || time.Since(start) > 2*time.Second {
			t.Fatal(err, time.Since(start))
		}
	})
	t.Run("ended request is capped at the shutdown timeout", func(t *testing.T) {
		_, conn := unixPair(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		start := time.Now()
		if err := s.writeResponse(ctx, conn, makeWriter(), buf); err == nil || time.Since(start) > 3*time.Second {
			t.Fatal(err, time.Since(start))
		}
	})
}
