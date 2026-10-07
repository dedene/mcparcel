package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

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
	// unauthorized is true while the last POST answer in OAuth mode was 401.
	unauthorized bool
	tap          *callTap
	limit        int // bytes per response; 0 means 16 MiB
	// refused is 1 while every POST so far was answered 400, 404 or 405,
	// -1 once any POST got another answer or no answer, 0 before the first.
	refused int
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
	limit := t.limit
	if limit <= 0 {
		limit = defaultMaxMessageBytes
	}
	tapped := false
	if clone.Method == http.MethodPost && clone.Body != nil && clone.Body != http.NoBody {
		clone.GetBody = nil
		if t.tap != nil {
			body, err := io.ReadAll(io.LimitReader(clone.Body, int64(limit)+1))
			_ = clone.Body.Close()
			if err != nil || len(body) > limit {
				return nil, output.NewError("connection_failed", nil)
			}
			clone.Body = io.NopCloser(bytes.NewReader(body))
			tapped = t.tap.sentBody(body)
		}
	}
	for k, v := range t.headers {
		if t.oauth && http.CanonicalHeaderKey(k) == "Authorization" {
			continue
		}
		clone.Header.Set(k, v)
	}
	resp, err := t.base.RoundTrip(clone)
	if clone.Method == http.MethodPost {
		t.mu.Lock()
		if err != nil || resp.StatusCode != 400 && resp.StatusCode != 404 && resp.StatusCode != 405 {
			t.refused = -1
		} else if t.refused == 0 {
			t.refused = 1
		}
		// Only the last answer counts: a resend the SDK authorized with a
		// new token and that then got 5xx, 403 or no answer is that failure,
		// not a rejected token.
		t.unauthorized = t.oauth && err == nil && resp.StatusCode == 401
		t.mu.Unlock()
	}
	if err != nil {
		return nil, output.NewError("connection_failed", nil)
	}
	// In OAuth mode a 401 or 403 goes on to the SDK's OAuth handler.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 || resp.StatusCode == 401 && !t.oauth {
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
	}
	resp.Body = &boundedResponseBody{ReadCloser: resp.Body, remaining: int64(limit), transport: t}
	if tapped {
		resp.Body = t.tap.capture(resp.Body, resp.StatusCode, resp.Header.Get("Content-Type"))
	}
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

// legacySSE returns runtime_unsupported when every POST was refused with
// 400, 404 or 405 and a GET on the endpoint answers 200 text/event-stream whose
// first 4 KiB contain an `event: endpoint` line. Anything else returns nil.
// It is diagnosis only: no MCP message is sent and nothing falls back.
func (t *headerTransport) legacySSE(ctx context.Context, endpoint string) error {
	t.mu.Lock()
	refused := t.refused
	t.mu.Unlock()
	if refused != 1 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := t.RoundTrip(req)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return nil
	}
	lines := bufio.NewScanner(io.LimitReader(resp.Body, 4096))
	for lines.Scan() {
		line := strings.TrimSpace(lines.Text())
		if strings.HasPrefix(line, ":") {
			continue
		}
		if name, ok := strings.CutPrefix(line, "event:"); ok && strings.TrimSpace(name) == "endpoint" {
			failure := output.NewError("runtime_unsupported", nil)
			failure.Message = "This server speaks only legacy SSE, which MCParcel does not support yet."
			return failure
		}
	}
	return nil
}

// makeTransport returns the transport, its cleanup, the sticky HTTP status and
// a diagnosis for a failed connect (nil for stdio and mode "streamable").
func makeTransport(opts ConnectOptions, tap *callTap) (mcp.Transport, func(context.Context), func() error, func(context.Context) error, error) {
	if opts.MaxMessageBytes <= 0 {
		opts.MaxMessageBytes = defaultMaxMessageBytes
	}
	if opts.Connection.Transport.Stdio != nil {
		transport, close, err := startProcess(opts, tap)
		return transport, close, func() error { return nil }, nil, err
	}
	if opts.Connection.Transport.HTTP == nil {
		return nil, nil, nil, nil, output.NewError("connection_failed", nil)
	}
	endpoint, err := config.LiteralText(opts.Connection.Transport.HTTP.URL)
	if err != nil {
		return nil, nil, nil, nil, output.NewError("config_required", nil)
	}
	origin, err := url.Parse(endpoint)
	if err != nil || origin.Host == "" || origin.User != nil || origin.Scheme != "http" && origin.Scheme != "https" {
		return nil, nil, nil, nil, output.NewError("connection_failed", nil)
	}
	headers := map[string]string{}
	for k, v := range opts.Headers {
		headers[k] = v
	}
	base := &http.Transport{Proxy: nil}
	rt := &headerTransport{base: base, origin: origin, headers: headers, oauth: opts.OAuth != nil, tap: tap, limit: opts.MaxMessageBytes}
	client := &http.Client{Transport: rt, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var diagnose func(context.Context) error
	if mode := opts.Connection.Transport.HTTP.Mode; mode == "" || mode == "auto" {
		diagnose = func(ctx context.Context) error { return rt.legacySSE(ctx, endpoint) }
	}
	return &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: client, MaxRetries: -1, DisableStandaloneSSE: true, MaxEventSize: opts.MaxMessageBytes + 1, OAuthHandler: opts.OAuth}, func(context.Context) { base.CloseIdleConnections() }, rt.status, diagnose, nil
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
		b.transport.tap.overflow()
		failure := output.NewError("protocol_error", nil)
		b.transport.mu.Lock()
		b.transport.failure = failure
		b.transport.mu.Unlock()
		return 0, failure
	}
	b.remaining -= int64(n)
	return n, err
}
