package catalog

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error { b.closed = true; return nil }
func routedClient(t *testing.T, handler http.HandlerFunc) *http.Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.github.com" {
			t.Errorf("unexpected host %s", r.URL.Host)
			return nil, errors.New("refused")
		}
		c := r.Clone(r.Context())
		u := *r.URL
		c.URL = &u
		c.URL.Scheme = "http"
		c.URL.Host = strings.TrimPrefix(s.URL, "http://")
		return s.Client().Transport.RoundTrip(c)
	})}
}

func TestPublicCatalogNoCredentials(t *testing.T) {
	t.Setenv("GH_TOKEN", "TOKEN-CANARY")
	t.Setenv("OP_SERVICE_ACCOUNT_TOKEN", "OP-CANARY")
	calls := 0
	runner := runnerFunc(func(context.Context, []string, io.Writer, io.Writer) error { calls++; return nil })
	_ = &GHAPI{Runner: runner}
	client := routedClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("credentials sent")
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" || r.Header.Get("User-Agent") != "mcparcel" {
			t.Error("headers")
		}
		_, _ = io.WriteString(w, "{}")
	})
	got, err := (&HTTPAPI{Client: client}).Get(t.Context(), "/repos/o/r")
	if err != nil || string(got.Body) != "{}" || calls != 0 {
		t.Fatalf("%+v %v %d", got, err, calls)
	}
	for _, endpoint := range []string{"https://evil/repos/o/r", "/repos/o/r#x", "/repos/o/r?token=x", "/repos/o\\r", "/repos/o/\nr"} {
		if _, err := (&HTTPAPI{Client: client}).Get(t.Context(), endpoint); !errors.Is(err, ErrContentInvalid) {
			t.Fatal(endpoint, err)
		}
	}
}

func TestHTTPResponseBounds(t *testing.T) {
	for _, kind := range []string{"chunked", "lying", "headers"} {
		t.Run(kind, func(t *testing.T) {
			b := &trackedBody{Reader: strings.NewReader(strings.Repeat("x", int(MaxAPIBytes+1)))}
			h := http.Header{}
			if kind == "headers" {
				b.Reader = strings.NewReader("{}")
				h.Set("X-Huge", strings.Repeat("x", int(MaxHeaderBytes)))
			}
			a := HTTPAPI{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: h, Body: b, ContentLength: 1}, nil
			})}}
			got, err := a.Get(t.Context(), "/repos/o/r")
			if !errors.Is(err, ErrContentTooLarge) || !b.closed || len(got.Body) != 0 {
				t.Fatalf("%v closed=%v", err, b.closed)
			}
		})
	}
}

func TestHTTPRedirectRefused(t *testing.T) {
	var count atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { count.Add(1) }))
	defer second.Close()
	a := HTTPAPI{Client: routedClient(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, second.URL, http.StatusFound) })}
	got, err := a.Get(t.Context(), "/repos/o/r")
	if err != nil || got.Status != 302 || count.Load() != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestHTTPDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	a := HTTPAPI{Client: routedClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })}
	if _, err := a.Get(ctx, "/repos/o/r"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := a.Get(ctx, "/repos/o/r"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestHTTPHeaderTransportBound(t *testing.T) {
	a := HTTPAPI{Client: routedClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Oversized", strings.Repeat("x", int(MaxHeaderBytes+1)))
		_, _ = io.WriteString(w, "{}")
	})}
	_, err := a.Get(t.Context(), "/repos/o/r")
	if !errors.Is(err, ErrContentTooLarge) {
		t.Fatal(err)
	}
}

func TestHTTPClientIsolation(t *testing.T) {
	client := routedClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "{}") })
	client.Timeout = time.Minute
	redirect := func(*http.Request, []*http.Request) error { return errors.New("caller redirect") }
	client.CheckRedirect = redirect
	tr := client.Transport
	_, err := (&HTTPAPI{Client: client}).Get(t.Context(), "/repos/o/r")
	if err != nil || client.Timeout != time.Minute || reflect.ValueOf(client.Transport).Pointer() != reflect.ValueOf(tr).Pointer() || reflect.ValueOf(client.CheckRedirect).Pointer() != reflect.ValueOf(redirect).Pointer() {
		t.Fatal("client mutated", err)
	}
	for _, endpoint := range []string{"/repos/o/../r", "/repos/o/%2e%2e", "/repos/o/%00", "/repos/o/%5c"} {
		if _, err := (&HTTPAPI{Client: client}).Get(t.Context(), endpoint); !errors.Is(err, ErrContentInvalid) {
			t.Fatal(endpoint, err)
		}
	}
}

func TestHTTPTransportHeaderLimit(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Huge", strings.Repeat("x", int(MaxHeaderBytes+1)))
		_, _ = io.WriteString(w, "{}")
	}))
	defer s.Close()
	transport := s.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = s.Certificate().DNSNames[0]
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "api.github.com:443" {
			return nil, errors.New("refused host")
		}
		return (&net.Dialer{}).DialContext(ctx, network, s.Listener.Addr().String())
	}
	_, err := (&HTTPAPI{Client: &http.Client{Transport: transport}}).Get(t.Context(), "/repos/o/r")
	if !errors.Is(err, ErrContentTooLarge) {
		t.Fatal(err)
	}
}
