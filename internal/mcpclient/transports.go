package mcpclient

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

type headerTransport struct {
	base    *http.Transport
	origin  *url.URL
	headers map[string]string
	oauth   bool // 401 and 403 reach the SDK's OAuth handler
	mu      sync.Mutex
	failure error
	// unauthorized records a 401 seen in OAuth mode until the next 2xx.
	unauthorized bool
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if failure := t.sticky(); failure != nil && req.Method != http.MethodDelete {
		return nil, failure
	}
	if req.URL.Scheme != t.origin.Scheme || req.URL.Host != t.origin.Host || req.URL.User != nil {
		return nil, output.NewError("connection_failed", nil)
	}
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	if clone.Method == http.MethodPost && clone.Body != nil && clone.Body != http.NoBody {
		clone.GetBody = nil
	}
	for k, v := range t.headers {
		if t.oauth && http.CanonicalHeaderKey(k) == "Authorization" {
			continue
		}
		clone.Header.Set(k, v)
	}
	resp, err := t.base.RoundTrip(clone)
	if err != nil {
		return nil, output.NewError("connection_failed", nil)
	}
	if t.oauth && (resp.StatusCode == 401 || resp.StatusCode == 403) {
		if resp.StatusCode == 401 {
			t.mu.Lock()
			t.unauthorized = true
			t.mu.Unlock()
		}
	} else if resp.StatusCode >= 300 && resp.StatusCode < 400 || resp.StatusCode == 401 {
		code := "connection_failed"
		if resp.StatusCode == 401 {
			code = "auth_required"
		}
		failure := output.NewError(code, nil)
		t.mu.Lock()
		t.failure = failure
		t.mu.Unlock()
		_ = resp.Body.Close()
		return nil, failure
	} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		t.mu.Lock()
		t.unauthorized = false
		t.mu.Unlock()
	}
	resp.Body = &boundedResponseBody{ReadCloser: resp.Body, remaining: 16 * 1024 * 1024, transport: t}
	return resp, nil
}
func (t *headerTransport) sticky() error { t.mu.Lock(); defer t.mu.Unlock(); return t.failure }

// status is the sticky failure, else auth_required after an unanswered 401.
func (t *headerTransport) status() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failure == nil && t.unauthorized {
		return output.NewError("auth_required", nil)
	}
	return t.failure
}

func makeTransport(opts ConnectOptions) (mcp.Transport, func(context.Context), func() error, error) {
	if opts.Connection.Transport.Stdio != nil {
		transport, close, err := startProcess(opts)
		return transport, close, func() error { return nil }, err
	}
	if opts.Connection.Transport.HTTP == nil {
		return nil, nil, nil, output.NewError("connection_failed", nil)
	}
	endpoint, err := config.LiteralText(opts.Connection.Transport.HTTP.URL)
	if err != nil {
		return nil, nil, nil, output.NewError("config_required", nil)
	}
	origin, err := url.Parse(endpoint)
	if err != nil || origin.Host == "" || origin.User != nil || origin.Scheme != "http" && origin.Scheme != "https" {
		return nil, nil, nil, output.NewError("connection_failed", nil)
	}
	headers := map[string]string{}
	for k, v := range opts.Headers {
		headers[k] = v
	}
	base := &http.Transport{Proxy: nil}
	rt := &headerTransport{base: base, origin: origin, headers: headers, oauth: opts.OAuth != nil}
	client := &http.Client{Transport: rt, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: client, MaxRetries: -1, DisableStandaloneSSE: true, MaxEventSize: 16 * 1024 * 1024, OAuthHandler: opts.OAuth}, func(context.Context) { base.CloseIdleConnections() }, rt.status, nil
}

type boundedResponseBody struct {
	io.ReadCloser
	remaining int64
	transport *headerTransport
}

func (b *boundedResponseBody) Read(p []byte) (int, error) {
	if int64(len(p)) > b.remaining+1 {
		p = p[:b.remaining+1]
	}
	n, err := b.ReadCloser.Read(p)
	if int64(n) > b.remaining {
		failure := output.NewError("protocol_error", nil)
		b.transport.mu.Lock()
		b.transport.failure = failure
		b.transport.mu.Unlock()
		return 0, failure
	}
	b.remaining -= int64(n)
	return n, err
}
