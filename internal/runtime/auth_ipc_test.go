package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/output"
)

func TestAuthRequestValidation(t *testing.T) {
	empty := args.Raw{Values: map[string]args.Value{}}
	withArg := args.Raw{Values: map[string]args.Value{"x": {}}}
	for name, tc := range map[string]struct {
		r  Request
		ok bool
	}{
		"login":                {Request{Method: "login", Connection: "a", Arguments: empty, NoInput: true}, true},
		"login no connection":  {Request{Method: "login", Arguments: empty}, false},
		"login tool":           {Request{Method: "login", Connection: "a", Tool: "t", Arguments: empty}, false},
		"login timeout":        {Request{Method: "login", Connection: "a", Timeout: "1s", Arguments: empty}, false},
		"login arguments":      {Request{Method: "login", Connection: "a", Arguments: withArg}, false},
		"login cached":         {Request{Method: "login", Connection: "a", Cached: true, Arguments: empty}, false},
		"login force":          {Request{Method: "login", Connection: "a", Force: true, Arguments: empty}, false},
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
				t.Fatal("login and logout need the work intent")
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
		"login meta":      {Request{Method: "login", Connection: "a", Arguments: empty, Meta: json.RawMessage(`{}`)}, false},
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

// loginHandler answers login with one auth_url frame through the daemon's
// sender, and logout with LogoutData.
type loginHandler struct{ daemonHandler }

func (h *loginHandler) Handle(ctx context.Context, id string, r Request, _ func() error) Response {
	if r.Method == "logout" {
		b, _ := json.Marshal(LogoutData{Connection: r.Connection, Removed: true})
		return Response{Data: b}
	}
	send := authURLSender(ctx)
	if r.Method != "login" || send == nil || !r.NoInput {
		return Response{Error: output.NewError("protocol_error", nil)}
	}
	if e := send("https://as.example/authorize?state=s"); e != nil {
		return Response{Error: output.NewError("canceled", nil)}
	}
	b, _ := json.Marshal(LoginData{Connection: "local:" + r.Connection, SignedIn: true})
	return Response{Data: b}
}

func TestClientDeliversAuthURL(t *testing.T) {
	c, _ := service(t, &loginHandler{}, 0)
	c.NoInput = true
	var mu sync.Mutex
	var urls []string
	c.OnAuthURL = func(u string) { mu.Lock(); urls = append(urls, u); mu.Unlock() }
	d, e := c.Login(testCtx(t), "a")
	if e != nil || d.Connection != "local:a" || !d.SignedIn {
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
