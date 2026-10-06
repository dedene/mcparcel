package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

func TestLegacyInitializationDoesNotOpenStandaloneSSE(t *testing.T) {
	get := make(chan struct{}, 1)
	release := make(chan struct{})
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			get <- struct{}{}
			<-release
			w.WriteHeader(405)
			return
		}
		if r.Method == "DELETE" {
			w.WriteHeader(200)
			return
		}
		var msg struct {
			Method string
			ID     json.RawMessage
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		if msg.Method != "initialize" {
			w.WriteHeader(202)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "fixture")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"fixture","version":"test"}}}`, msg.ID)
	}))
	defer hs.Close()
	tr, cleanup, _, err := makeTransport(ConnectOptions{Connection: config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal(hs.URL)}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(context.Background())
	done := make(chan *mcp.ClientSession, 1)
	go func() {
		s, e := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil).Connect(context.Background(), tr, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
		if e != nil {
			done <- nil
		} else {
			done <- s
		}
	}()
	select {
	case <-get:
		close(release)
		s := <-done
		if s != nil {
			s.Close()
		}
		t.Fatal("initialization entered detached standalone GET")
	case s := <-done:
		close(release)
		if s == nil {
			t.Fatal("connect failed")
		}
		s.Close()
	case <-time.After(time.Second):
		close(release)
		t.Fatal("initialization stuck")
	}
}

func TestHTTPErrorResponseBodyBounded(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(500)
		_, _ = io.WriteString(w, strings.Repeat("x", 16*1024*1024+1))
	}))
	defer hs.Close()
	origin, _ := url.Parse(hs.URL)
	base := &http.Transport{}
	defer base.CloseIdleConnections()
	tr := &headerTransport{base: base, origin: origin}
	req, _ := http.NewRequestWithContext(context.Background(), "POST", hs.URL, strings.NewReader("{}"))
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err == nil || len(body) > 16*1024*1024 {
		t.Fatal("unbounded error body", len(body), err)
	}
	var safe *output.Error
	if !errors.As(err, &safe) || safe.Code != "protocol_error" {
		t.Fatal(err)
	}
}

func TestHTTPFailureStillAllowsSessionCleanup(t *testing.T) {
	var deletes atomic.Int32
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
		}
		w.WriteHeader(200)
	}))
	defer hs.Close()
	origin, _ := url.Parse(hs.URL)
	base := &http.Transport{}
	defer base.CloseIdleConnections()
	tr := &headerTransport{base: base, origin: origin, failure: output.NewError("protocol_error", nil)}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodDelete, hs.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal("cleanup rejected after protocol failure", err)
	}
	resp.Body.Close()
	req, _ = http.NewRequestWithContext(context.Background(), http.MethodPost, hs.URL, strings.NewReader("{}"))
	if _, err := tr.RoundTrip(req); err == nil {
		t.Fatal("work admitted after protocol failure")
	}
	if deletes.Load() != 1 {
		t.Fatal("session DELETE missing")
	}
}
