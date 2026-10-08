package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
)

func TestAuthRequestValidation(t *testing.T) {
	empty := args.Raw{Values: map[string]args.Value{}}
	withArg := args.Raw{Values: map[string]args.Value{"x": {}}}
	for name, tc := range map[string]struct {
		r  Request
		ok bool
	}{
		"auth":                 {Request{Method: "auth", Connection: "a", Arguments: empty, NoInput: true}, true},
		"auth no connection":   {Request{Method: "auth", Arguments: empty}, false},
		"auth tool":            {Request{Method: "auth", Connection: "a", Tool: "t", Arguments: empty}, false},
		"auth timeout":         {Request{Method: "auth", Connection: "a", Timeout: "1s", Arguments: empty}, false},
		"auth arguments":       {Request{Method: "auth", Connection: "a", Arguments: withArg}, false},
		"auth cached":          {Request{Method: "auth", Connection: "a", Cached: true, Arguments: empty}, false},
		"auth force":           {Request{Method: "auth", Connection: "a", Force: true, Arguments: empty}, false},
		"logout":               {Request{Method: "logout", Connection: "local:a", Arguments: empty}, true},
		"logout non-canonical": {Request{Method: "logout", Connection: "a", Arguments: empty}, false},
		"logout force":         {Request{Method: "logout", Connection: "local:a", Force: true, Arguments: empty}, false},
		"logout tool":          {Request{Method: "logout", Connection: "local:a", Tool: "t", Arguments: empty}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if e := validateRequest("work", tc.r); (e == nil) != tc.ok {
				t.Fatal(e)
			}
			if validateRequest("status", tc.r) == nil {
				t.Fatal("auth and logout need the work intent")
			}
			if e := validateBody(frame("request", tc.r)); (e == nil) != tc.ok {
				t.Fatal(e)
			}
		})
	}
}

func TestCallMetaWireValidation(t *testing.T) {
	empty := args.Raw{Values: map[string]args.Value{}}
	for name, tc := range map[string]struct {
		r  Request
		ok bool
	}{
		"call meta":       {Request{Method: "call", Tool: "t", Arguments: empty, Meta: json.RawMessage(`{"x-codex-turn-metadata":{"turn_id":"1"}}`)}, true},
		"call no meta":    {Request{Method: "call", Tool: "t", Arguments: empty}, true},
		"call array":      {Request{Method: "call", Tool: "t", Arguments: empty, Meta: json.RawMessage(`[1]`)}, false},
		"call null":       {Request{Method: "call", Tool: "t", Arguments: empty, Meta: json.RawMessage(`null`)}, false},
		"call reserved":   {Request{Method: "call", Tool: "t", Arguments: empty, Meta: json.RawMessage(`{"progressToken":"p"}`)}, false},
		"tools meta":      {Request{Method: "tools", Connection: "a", Arguments: empty, Meta: json.RawMessage(`{}`)}, false},
		"auth meta":       {Request{Method: "auth", Connection: "a", Arguments: empty, Meta: json.RawMessage(`{}`)}, false},
		"status meta":     {Request{Method: "status", Arguments: empty, Meta: json.RawMessage(`{}`)}, false},
		"call large meta": {Request{Method: "call", Tool: "t", Arguments: empty, Meta: json.RawMessage(`{"k":"` + strings.Repeat("a", args.MaxMetaBytes) + `"}`)}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if e := validateBody(frame("request", tc.r)); (e == nil) != tc.ok {
				t.Fatal(e)
			}
		})
	}
}

func TestAuthURLFrameValidation(t *testing.T) {
	if e := validateBody(frame("auth_url", AuthURL{URL: "https://as.example/authorize"})); e != nil {
		t.Fatal(e)
	}
	for _, body := range []string{`{"url":""}`, `{}`, `{"url":"x","extra":1}`} {
		if validateBody(Frame{ProtocolVersion, "auth_url", testID, json.RawMessage(body)}) == nil {
			t.Fatal("accepted", body)
		}
	}
}

// loginHandler answers auth with one auth_url frame through the daemon's
// sender, and logout with LogoutData.
type loginHandler struct{ daemonHandler }

func (h *loginHandler) Handle(ctx context.Context, id string, r Request, _ func() error) Response {
	if r.Method == "logout" {
		b, _ := json.Marshal(LogoutData{Connection: r.Connection, Removed: true})
		return Response{Data: b}
	}
	send := authURLSender(ctx)
	if r.Method != "auth" || send == nil || !r.NoInput {
		return Response{Error: output.NewError("protocol_error", nil)}
	}
	if e := send("https://as.example/authorize?state=s"); e != nil {
		return Response{Error: output.NewError("canceled", nil)}
	}
	b, _ := json.Marshal(AuthData{Connection: "local:" + r.Connection, SignedIn: ptr(true)})
	return Response{Data: b}
}

func TestClientDeliversAuthURL(t *testing.T) {
	c, _ := service(t, &loginHandler{}, 0)
	c.NoInput = true
	var mu sync.Mutex
	var urls []string
	c.OnAuthURL = func(u string) { mu.Lock(); urls = append(urls, u); mu.Unlock() }
	d, e := c.Auth(testCtx(t), "a")
	if e != nil || d.Connection != "local:a" || d.SignedIn == nil || !*d.SignedIn || d.SecretsRefreshed != nil || d.TokenRenewed != nil {
		t.Fatal(d, e)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(urls) != 1 || urls[0] != "https://as.example/authorize?state=s" {
		t.Fatal(urls)
	}
	out, e := c.Logout(testCtx(t), "local:a")
	if e != nil || out.Connection != "local:a" || !out.Removed || out.ProviderRevoked {
		t.Fatal(out, e)
	}
}

func TestLockAuthFrames(t *testing.T) {
	empty := args.Raw{Values: map[string]args.Value{}}
	for name, tc := range map[string]struct {
		r  Request
		ok bool
	}{
		"lock":               {Request{Method: "lock", Arguments: empty}, true},
		"lock connection":    {Request{Method: "lock", Connection: "local:a", Arguments: empty}, false},
		"lock tool":          {Request{Method: "lock", Tool: "t", Arguments: empty}, false},
		"lock force":         {Request{Method: "lock", Force: true, Arguments: empty}, false},
		"lock prompt":        {Request{Method: "lock", Prompt: "terminal", Arguments: empty}, false},
		"auth canonical":     {Request{Method: "auth", Connection: "local:a", Arguments: empty}, true},
		"auth non-canonical": {Request{Method: "auth", Connection: "a", Arguments: empty}, true},
		"auth no input":      {Request{Method: "auth", Connection: "a", NoInput: true, Arguments: empty}, true},
		"auth prompt":        {Request{Method: "auth", Connection: "a", Prompt: "terminal", Arguments: empty}, false},
		"refresh":            {Request{Method: "refresh", Connection: "local:a", Arguments: empty}, false},
		"login":              {Request{Method: "login", Connection: "a", Arguments: empty}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if e := validateBody(frame("request", tc.r)); (e == nil) != tc.ok {
				t.Fatal(e)
			}
			if !tc.ok && validateRequest("work", tc.r) != ErrInvalidFrame {
				t.Fatal("not refused as an invalid frame")
			}
			if validateRequest("status", tc.r) == nil {
				t.Fatal("lock and auth need the work intent")
			}
		})
	}
}

// credentialHandler answers lock and auth.
type credentialHandler struct{ daemonHandler }

func (h *credentialHandler) Handle(_ context.Context, _ string, r Request, _ func() error) Response {
	var v any = LockData{Locked: true}
	if r.Method == "auth" {
		v = AuthData{Connection: "local:" + r.Connection, SecretsRefreshed: ptr(true)}
	}
	b, _ := json.Marshal(v)
	return Response{Data: b}
}

func TestClientLockAuth(t *testing.T) {
	c, _ := service(t, &credentialHandler{}, 0)
	if d, e := c.Lock(testCtx(t)); e != nil || !d.Locked {
		t.Fatal(d, e)
	}
	if d, e := c.Auth(testCtx(t), "a"); e != nil || d.Connection != "local:a" || d.SecretsRefreshed == nil || !*d.SecretsRefreshed || d.SignedIn != nil || d.TokenRenewed != nil {
		t.Fatal(d, e)
	}
	// Unlike the old refresh, auth starts the runtime: a client that cannot
	// start one fails instead of answering that nothing was cached.
	p, _ := testutil.IsolatedPaths(t)
	stopped := &Client{Paths: p, Version: "dev", Executable: "/nonexistent"}
	if d, e := stopped.Auth(testCtx(t), "a"); e == nil {
		t.Fatal("auth did not try to start the runtime", d)
	}
}
