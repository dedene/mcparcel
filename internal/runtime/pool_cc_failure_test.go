package runtime

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

// ccClock lets a test move the pool's clock past the token's refresh point
// (720 s for the rig's 900 s token) while the token stays valid at the
// authorization server, which uses the real clock.
func ccClock(r *poolRig) func(time.Duration) {
	base := time.Now()
	var offset atomic.Int64
	r.opts.Now = func() time.Time { return base.Add(time.Duration(offset.Load())) }
	return func(d time.Duration) { offset.Add(int64(d)) }
}

// warmCC opens the pooled session and mints its token without running a tool.
func warmCC(t *testing.T, r *poolRig) {
	t.Helper()
	success(t, r.h.Handle(testCtx(t), testID, Request{Method: "tools", Connection: "a"}, nil))
}

// tokenEndpointDown checks the answer to a call whose token request got 503:
// a retryable connection_failed naming the token endpoint, never
// auth_failed, token_rejected or outcome_unknown, and the tool did not run.
func tokenEndpointDown(t *testing.T, r *poolRig, s *ccServer, res Response) {
	t.Helper()
	responseCode(t, res, "connection_failed", false)
	if !strings.Contains(res.Error.Message, "Could not get an access token") || strings.Contains(res.Error.Message, "token_rejected") {
		t.Fatalf("%+v", res.Error)
	}
	if res.Error.Details != nil && res.Error.Details.Outcome != "" {
		t.Fatalf("%+v", res.Error.Details)
	}
	noCCLeak(t, responseText(res))
	found := false
	for _, line := range s.logged() {
		found = found || line == `oauth_token_mint_failed {"code":"http_5xx","status":503}`
	}
	if !found {
		t.Fatal(s.logged())
	}
	s.tokenDown.Store(false)
	if n := count(t, r.call(testCtx(t), "a", "counter")); n != 1 {
		t.Fatal("the failed call ran", n-1, "times")
	}
}

// A token endpoint outage while a pooled session refreshes its token is
// retryable connection_failed, in tools/list and in tools/call.
func TestCCRefreshTokenEndpointDown(t *testing.T) {
	t.Run("tools/list", func(t *testing.T) {
		r, s := ccRig(t, true)
		advance := ccClock(r)
		r.start()
		warmCC(t, r)
		advance(800 * time.Second)
		s.tokenDown.Store(true)
		tokenEndpointDown(t, r, s, r.call(testCtx(t), "a", "counter"))
	})
	t.Run("tools/call", func(t *testing.T) {
		r, s := ccRig(t, true)
		advance := ccClock(r)
		r.start()
		warmCC(t, r)
		// The refresh point falls between tools/list and tools/call: the
		// request is never sent, so its outcome is known.
		tokenEndpointDown(t, r, s, callBefore(r, func() {
			advance(800 * time.Second)
			s.tokenDown.Store(true)
		}))
	})
}

// The server's 401 drops the token; when minting its replacement gets 503,
// nothing was rejected: connection_failed, not token_rejected.
func TestCC401RemintTokenEndpointDown(t *testing.T) {
	r, s := ccRig(t, true)
	r.start()
	warmCC(t, r)
	tokenEndpointDown(t, r, s, callBefore(r, func() {
		s.as.Revoke()
		s.tokenDown.Store(true)
	}))
}

func TestCCTokenEndpointDownAtConnect(t *testing.T) {
	r, s := ccRig(t, true)
	s.tokenDown.Store(true)
	r.start()
	tokenEndpointDown(t, r, s, r.call(testCtx(t), "a", "counter"))
}

// A desktop client_credentials connection whose secret is a 1Password
// reference, called with --no-input before 1Password was opened, needs
// input: auth_required stays, no token is requested, nothing is "rejected".
func TestCCOnePasswordNoInputKeepsAuthRequired(t *testing.T) {
	r, s := ccRig(t, false)
	r.opts.Credentials = auth.NewResolver(auth.ResolverOptions{Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) {
		t.Error("1Password opened despite --no-input")
		return nil, auth.ErrProvider
	}}})
	c := r.personal.Connections["a"]
	c.CredentialProfile = "shared"
	c.Auth.ClientSecret = &config.Value{Secret: &config.SecretRef{Secret: "op://vault/front/secret"}}
	r.personal.Connections["a"] = c
	r.personal.CredentialProfiles["shared"] = config.ProfileRequirement{}
	r.local.CredentialProfiles["shared"] = config.Profile{Mode: "desktop-service-account", Account: "fixture", BootstrapRef: "op://vault/bootstrap/token", SessionDuration: "24h"}
	r.start()
	res := r.h.Handle(testCtx(t), testID, Request{Method: "call", Connection: "a", Tool: "counter", NoInput: true, Arguments: emptyArgs()}, nil)
	responseCode(t, res, "auth_required", false)
	if strings.Contains(res.Error.Message, "token_rejected") || res.Error.NextAction != "Run the command interactively with 1Password desktop integration enabled." {
		t.Fatalf("%+v", res.Error)
	}
	if s.grants() != 0 || r.connects.Load() != 0 {
		t.Fatal("grants", s.grants(), "connects", r.connects.Load())
	}
}

// The server's 401 drops the token; when the call's deadline passes while
// its replacement is still being minted, no new token was issued and the
// request was not resent: timeout, not dispatched, never token_rejected.
func TestCC401RemintDeadline(t *testing.T) {
	timedOut := func(t *testing.T, r *poolRig, s *ccServer, res Response) {
		t.Helper()
		responseCode(t, res, "timeout", false)
		if strings.Contains(res.Error.Message, "token_rejected") || res.Error.Details != nil && res.Error.Details.Outcome != "" {
			t.Fatalf("%+v %+v", res.Error, res.Error.Details)
		}
		noCCLeak(t, responseText(res))
		s.tokenHang.Store(false)
		if n := count(t, r.call(testCtx(t), "a", "counter")); n != 1 {
			t.Fatal("the timed-out call ran", n-1, "times")
		}
	}
	short := Request{Method: "call", Connection: "a", Tool: "counter", Timeout: "500ms", Arguments: emptyArgs()}
	t.Run("tools/call", func(t *testing.T) {
		r, s := ccRig(t, true)
		r.start()
		warmCC(t, r)
		timedOut(t, r, s, r.h.Handle(testCtx(t), testID, short, func() error {
			s.as.Revoke()
			s.tokenHang.Store(true)
			return nil
		}))
	})
	t.Run("tools/list", func(t *testing.T) {
		r, s := ccRig(t, true)
		r.start()
		warmCC(t, r)
		s.as.Revoke()
		s.tokenHang.Store(true)
		timedOut(t, r, s, r.h.Handle(testCtx(t), testID, short, nil))
	})
}

// A resend that a new token authorized and that then got a non-401 answer
// is that answer's error, not token_rejected: the server took the token.
func TestCC401ResendServerError(t *testing.T) {
	r, s := ccRig(t, true)
	r.start()
	warmCC(t, r)
	res := callBefore(r, func() { s.answer(401, 503) })
	if res.Error == nil || res.Error.Code == "auth_failed" || res.Error.Code == "auth_required" || strings.Contains(res.Error.Message, "token_rejected") {
		t.Fatalf("%+v", res.Error)
	}
	if s.grants() != 2 {
		t.Fatal("grants", s.grants())
	}
	noCCLeak(t, responseText(res))
}
