package catalog

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

type runnerFunc func(context.Context, []string, io.Writer, io.Writer) error

func (f runnerFunc) Run(c context.Context, a []string, o, e io.Writer) error { return f(c, a, o, e) }
func TestPrivateCatalogUsesGH(t *testing.T) {
	a := GHAPI{Runner: runnerFunc(func(_ context.Context, argv []string, out, _ io.Writer) error {
		want := []string{"api", "--hostname", "github.com", "--method", "GET", "--include", "-H", "Accept: application/vnd.github+json", "-H", "X-GitHub-Api-Version: 2026-03-10", "/repos/o/r"}
		if !reflect.DeepEqual(argv, want) {
			t.Fatalf("%q", argv)
		}
		_, err := io.WriteString(out, "HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n{}")
		return err
	})}
	got, err := a.Get(t.Context(), "/repos/o/r")
	if err != nil || got.Status != 200 || string(got.Body) != "{}" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestGHFailureSanitized(t *testing.T) {
	for _, tc := range []struct {
		name, raw     string
		failure, want error
		status        int
	}{
		{"missing", "", exec.ErrNotFound, ErrGHRequired, 0},
		{"stderr", "", errors.New("TOKEN-CANARY"), ErrUnauthorized, 0},
		{"http", "HTTP/1.1 401 Unauthorized\r\n\r\n{}", errors.New("exit1"), nil, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := GHAPI{Runner: runnerFunc(func(_ context.Context, _ []string, o, e io.Writer) error {
				_, _ = io.WriteString(e, "TOKEN-CANARY")
				_, _ = io.WriteString(o, tc.raw)
				return tc.failure
			})}
			got, err := a.Get(t.Context(), "/repos/o/r")
			if !errors.Is(err, tc.want) || got.Status != tc.status {
				t.Fatalf("%+v %v", got, err)
			}
			if err != nil && strings.Contains(err.Error(), "CANARY") {
				t.Fatal(err)
			}
		})
	}
}

func TestGHOutputBound(t *testing.T) {
	finished := make(chan struct{})
	a := GHAPI{Runner: runnerFunc(func(ctx context.Context, _ []string, o, _ io.Writer) error {
		defer close(finished)
		_, _ = io.WriteString(o, strings.Repeat("x", int(MaxAPIBytes+MaxHeaderBytes+2)))
		<-ctx.Done()
		return ctx.Err()
	})}
	_, err := a.Get(t.Context(), "/repos/o/r")
	if !errors.Is(err, ErrContentTooLarge) {
		t.Fatal(err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("runner still running")
	}
}

func TestGHBodyAndHeaderBounds(t *testing.T) {
	for _, kind := range []string{"body", "lying-length", "headers"} {
		t.Run(kind, func(t *testing.T) {
			raw := "HTTP/1.1 200 OK\r\n\r\n" + strings.Repeat("x", int(MaxAPIBytes+1))
			if kind == "lying-length" {
				raw = "HTTP/1.1 200 OK\r\nContent-Length: 1\r\n\r\n" + strings.Repeat("x", int(MaxAPIBytes+1))
			}
			if kind == "headers" {
				raw = "HTTP/2.0 200 OK\r\nX-Huge: " + strings.Repeat("x", int(MaxHeaderBytes)) + "\r\n\r\n{}"
			}
			a := GHAPI{Runner: runnerFunc(func(_ context.Context, _ []string, o, _ io.Writer) error {
				_, err := io.WriteString(o, raw)
				return err
			})}
			got, err := a.Get(t.Context(), "/repos/o/r")
			if !errors.Is(err, ErrContentTooLarge) || len(got.Body) != 0 {
				t.Fatal(err, len(got.Body))
			}
		})
	}
}
