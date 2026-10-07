//go:build linux && headless_e2e

package headless_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The fake Front listens on fixed loopback ports that testdata/personal.json
// names: the workspace token endpoint and the bearer-protected MCP server run
// on separate ports, as they run on separate hosts at Front.
const (
	tokenAddr    = "127.0.0.1:18080"
	mcpAddr      = "127.0.0.1:18081"
	clientID     = "front-id"
	clientSecret = "front-secret"
	tokenPrefix  = "e2e-front-token-"
)

// counts is a snapshot of what reached the fake Front.
type counts struct {
	Grants       int            // client_credentials tokens issued
	Unauthorized int            // 401 answers from the MCP endpoint
	MCP          int            // requests that reached the MCP endpoint
	Tools        map[string]int // tool invocations by name
}

// fakeFront is a client_credentials token endpoint (client_secret_post or
// client_secret_basic) and a Streamable HTTP MCP server that admits only
// requests with a live token from that endpoint.
type fakeFront struct {
	mu        sync.Mutex
	expiresIn int
	live      map[string]time.Time
	issued    []string
	c         counts
	servers   []*http.Server
}

func startFakeFront() (*fakeFront, error) {
	f := &fakeFront{expiresIn: 900, live: map[string]time.Time{}, c: counts{Tools: map[string]int{}}}
	token := http.NewServeMux()
	token.HandleFunc("POST /oauth/token", f.token)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return f.server() }, nil)
	for addr, h := range map[string]http.Handler{tokenAddr: token, mcpAddr: f.protect(mcpHandler)} {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			f.close()
			return nil, err
		}
		s := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
		f.servers = append(f.servers, s)
		go func() { _ = s.Serve(ln) }()
	}
	return f, nil
}

func (f *fakeFront) close() {
	for _, s := range f.servers {
		_ = s.Close()
	}
}

// setExpiresIn sets expires_in for tokens issued from now on.
func (f *fakeFront) setExpiresIn(seconds int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expiresIn = seconds
}

// revoke invalidates every issued token, as a rotated client secret would.
func (f *fakeFront) revoke() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.live = map[string]time.Time{}
}

func (f *fakeFront) counts() counts {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.c
	c.Tools = map[string]int{}
	for k, v := range f.c.Tools {
		c.Tools[k] = v
	}
	return c
}

// issuedTokens returns every access token the endpoint ever issued.
func (f *fakeFront) issuedTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.issued...)
}

func (f *fakeFront) token(w http.ResponseWriter, r *http.Request) {
	if r.ParseForm() != nil || r.PostForm.Get("grant_type") != "client_credentials" {
		writeJSON(w, 400, map[string]string{"error": "unsupported_grant_type"})
		return
	}
	id, secret, basic := r.BasicAuth()
	if !basic {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != clientID || secret != clientSecret {
		writeJSON(w, 401, map[string]string{"error": "invalid_client"})
		return
	}
	f.mu.Lock()
	access := tokenPrefix + rand.Text()
	f.live[access] = time.Now().Add(time.Duration(f.expiresIn) * time.Second)
	f.issued = append(f.issued, access)
	f.c.Grants++
	expiresIn := f.expiresIn
	f.mu.Unlock()
	writeJSON(w, 200, map[string]any{"access_token": access, "token_type": "bearer", "expires_in": expiresIn})
}

// protect counts every MCP request and answers 401 (RFC 6750 invalid_token)
// to a missing, unknown, revoked or expired token.
func (f *fakeFront) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		f.c.MCP++
		expiry, ok := f.live[token]
		ok = ok && time.Now().Before(expiry)
		if !ok {
			f.c.Unauthorized++
		}
		f.mu.Unlock()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (f *fakeFront) invoked(tool string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.c.Tools[tool]++
}

type conversationInput struct {
	ID string `json:"id"`
}

type draftInput struct {
	ConversationID string `json:"conversation_id"`
	Body           string `json:"body"`
}

type messageInput struct {
	To   string `json:"to"`
	Body string `json:"body"`
}

type approvalInput struct{}

type textOutput struct {
	Text string `json:"text"`
}

// server is a fresh MCP server per session with Front's tool names.
// send_message must never run: mcparcel's toolPolicy denies it.
func (f *fakeFront) server() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "fake-front", Version: "0.0.0"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "read_conversation", Description: "Read a conversation."},
		func(_ context.Context, _ *mcp.CallToolRequest, in conversationInput) (*mcp.CallToolResult, textOutput, error) {
			f.invoked("read_conversation")
			return nil, textOutput{Text: "conversation " + in.ID}, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "create_draft", Description: "Create a draft reply."},
		func(_ context.Context, _ *mcp.CallToolRequest, in draftInput) (*mcp.CallToolResult, textOutput, error) {
			f.invoked("create_draft")
			return nil, textOutput{Text: "draft on " + in.ConversationID}, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "send_message", Description: "Send a message."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ messageInput) (*mcp.CallToolResult, textOutput, error) {
			f.invoked("send_message")
			return nil, textOutput{Text: "sent"}, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "ask_approval", Description: "Ask the user to approve before acting."},
		func(ctx context.Context, req *mcp.CallToolRequest, _ approvalInput) (*mcp.CallToolResult, textOutput, error) {
			f.invoked("ask_approval")
			res, err := req.Session.Elicit(ctx, &mcp.ElicitParams{
				Message:         "Send the reply to the customer?",
				RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{"approve": map[string]any{"type": "boolean"}}},
			})
			if err != nil {
				return nil, textOutput{}, errors.New("elicitation failed")
			}
			return nil, textOutput{Text: "action=" + res.Action}, nil
		})
	return s
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
