package auth_test

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
)

const explainURL = "https://mcp.example.test/mcp"

var explainNow = time.Unix(1_760_000_000, 0)

func signedInState() auth.OAuthState {
	return auth.OAuthState{Version: 1, URL: explainURL, Issuer: "https://as.example.test", TokenURL: "https://as.example.test/token", RefreshToken: "rt"}
}

func failedState(code string) auth.OAuthState {
	s := signedInState()
	s.RefreshToken = ""
	s.Failure = &auth.OAuthFailure{At: explainNow.Unix(), Code: code}
	return s
}

func ev(daysAgo int64, kind auth.HealthKind, mod ...func(*auth.HealthEvent)) auth.HealthEvent {
	e := auth.HealthEvent{At: explainNow.Unix() - daysAgo*86400, Kind: kind}
	for _, m := range mod {
		m(&e)
	}
	return e
}

func terminal(code string) func(*auth.HealthEvent) {
	return func(e *auth.HealthEvent) { e.Code, e.Terminal = code, true }
}

func TestExplainCauses(t *testing.T) {
	authorized := ev(20, auth.HealthAuthorized, func(e *auth.HealthEvent) { e.RefreshToken = true; e.RefreshTTL = 14 * 86400 })
	for _, tc := range []struct {
		name, want, message string
		found               bool
		state               auth.OAuthState
		events              []auth.HealthEvent
		keepAlive           string
	}{
		{name: "signed out", want: "signed_out", events: []auth.HealthEvent{authorized, ev(1, auth.HealthLogout)}},
		{name: "keychain missing", want: "keychain_missing", events: []auth.HealthEvent{authorized}},
		{name: "never signed in", want: "never_signed_in"},
		{
			name: "server requested", want: "server_requested", found: true, state: auth.OAuthState{Version: 1, URL: explainURL},
			events: []auth.HealthEvent{ev(0, auth.HealthReauthorizationRequired, terminal("server_requested"))},
		},
		{name: "url changed", want: "url_changed", found: true, state: func() auth.OAuthState { s := signedInState(); s.URL = "https://old.example.test/mcp"; return s }()},
		{
			name: "interrupted", want: "interrupted_refresh", found: true, state: failedState("invalid_grant"),
			events: []auth.HealthEvent{authorized, ev(0, auth.HealthRefreshFailed, terminal("invalid_grant"), func(e *auth.HealthEvent) { e.Interrupted = true })},
		},
		{
			name: "expired idle", want: "refresh_expired_or_revoked", message: "for 15 days. Keep-alive was off. Its known lifetime of 14 days had passed.", found: true, state: failedState("invalid_grant"), keepAlive: "off",
			events: []auth.HealthEvent{authorized, ev(5, auth.HealthRefreshFailed, terminal("invalid_grant"), func(e *auth.HealthEvent) { e.Idle = 15 * 86400 })},
		},
		{
			name: "client rejected", want: "client_rejected", message: "(unauthorized_client)", found: true, state: failedState("unauthorized_client"),
			events: []auth.HealthEvent{authorized, ev(0, auth.HealthRefreshFailed, terminal("unauthorized_client"))},
		},
		{
			name: "issuer changed", want: "issuer_changed", found: true, state: failedState("issuer_changed"),
			events: []auth.HealthEvent{authorized, ev(0, auth.HealthReauthorizationRequired, terminal("issuer_changed"))},
		},
		{
			name: "token rejected", want: "token_rejected", found: true, state: failedState("token_rejected"),
			events: []auth.HealthEvent{authorized, ev(0, auth.HealthReauthorizationRequired, terminal("token_rejected"))},
		},
		{
			name: "other code", want: "refresh_rejected", message: "(invalid_scope)", found: true, state: failedState("invalid_scope"),
			events: []auth.HealthEvent{authorized, ev(0, auth.HealthRefreshFailed, terminal("invalid_scope"))},
		},
		{name: "before stage 7b", want: "refresh_expired_or_revoked", found: true, state: failedState("invalid_grant")},
		{name: "before stage 7b client", want: "client_rejected", found: true, state: failedState("invalid_client")},
		{
			name: "no refresh token", want: "no_refresh_token", found: true, state: func() auth.OAuthState { s := signedInState(); s.RefreshToken = ""; return s }(),
			events: []auth.HealthEvent{ev(1, auth.HealthAuthorized)},
		},
		{
			name: "refresh failing", want: "refresh_failing", message: "(temporarily_unavailable)", found: true, state: signedInState(),
			events: []auth.HealthEvent{authorized, ev(0, auth.HealthRefreshFailed, func(e *auth.HealthEvent) { e.Code, e.HTTPStatus = "temporarily_unavailable", 503 })},
		},
		{
			name: "signed in after a failure", found: true, state: signedInState(),
			events: []auth.HealthEvent{ev(3, auth.HealthRefreshFailed, terminal("invalid_grant")), ev(2, auth.HealthAuthorized, func(e *auth.HealthEvent) { e.RefreshToken = true })},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := auth.ExplainSession(auth.SessionInput{Connection: "local:a", Name: "a", URL: explainURL, State: tc.state, Found: tc.found, Health: auth.ConnectionHealth{Events: tc.events}, KeepAlive: tc.keepAlive, Now: explainNow})
			switch {
			case tc.want == "" && r.Cause != nil:
				t.Fatalf("cause %+v", r.Cause)
			case tc.want == "":
			case r.Cause == nil || r.Cause.Code != tc.want || !strings.Contains(r.Cause.Message, tc.message):
				t.Fatalf("cause %+v, want %s %q", r.Cause, tc.want, tc.message)
			}
		})
	}
}

func TestExplainStates(t *testing.T) {
	refreshed := func(daysAgo, ttlDays int64) auth.HealthEvent {
		return ev(daysAgo, auth.HealthRefreshed, func(e *auth.HealthEvent) { e.RefreshTTL = ttlDays * 86400 })
	}
	for _, tc := range []struct {
		name, want string
		state      auth.OAuthState
		events     []auth.HealthEvent
	}{
		{"ok without known expiry", auth.StateOK, signedInState(), []auth.HealthEvent{refreshed(1, 0)}},
		{"ok far from expiry", auth.StateOK, signedInState(), []auth.HealthEvent{refreshed(1, 14)}},
		{"expiring within 72h", auth.StateExpiring, signedInState(), []auth.HealthEvent{refreshed(12, 14)}},
		{"expiring after a transient failure", auth.StateExpiring, signedInState(), []auth.HealthEvent{refreshed(1, 0), ev(0, auth.HealthRefreshFailed, func(e *auth.HealthEvent) { e.Code = "network_error" })}},
		{"sign-in required", auth.StateSignInRequired, failedState("invalid_grant"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := auth.ExplainSession(auth.SessionInput{Connection: "local:a", Name: "a", URL: explainURL, State: tc.state, Found: true, Health: auth.ConnectionHealth{Events: tc.events}, Now: explainNow})
			if r.State != tc.want {
				t.Fatalf("state %q, want %q (%+v)", r.State, tc.want, r.Cause)
			}
			if (r.NextAction == "mcparcel auth login a") != (tc.want == auth.StateSignInRequired) {
				t.Fatal(r.NextAction)
			}
		})
	}
	r := auth.ExplainSession(auth.SessionInput{Connection: "local:a", Name: "a", URL: explainURL, State: signedInState(), Found: true, Health: auth.ConnectionHealth{Events: []auth.HealthEvent{refreshed(1, 14)}}, Now: explainNow})
	if r.LastRefreshAt != time.Unix(explainNow.Unix()-86400, 0).UTC().Format(time.RFC3339) || r.RefreshTokenExpiresAt != time.Unix(explainNow.Unix()+13*86400, 0).UTC().Format(time.RFC3339) {
		t.Fatalf("%+v", r)
	}
}

// After a re-login the session is fine, but auth status still says why that
// sign-in was needed.
func TestExplainPreviousCause(t *testing.T) {
	authorized := ev(30, auth.HealthAuthorized, func(e *auth.HealthEvent) { e.RefreshToken = true })
	revoked := ev(2, auth.HealthRefreshFailed, terminal("invalid_grant"), func(e *auth.HealthEvent) { e.Idle = 15 * 86400 })
	relogin := ev(1, auth.HealthAuthorized, func(e *auth.HealthEvent) { e.RefreshToken = true })
	for _, tc := range []struct {
		name, want string
		events     []auth.HealthEvent
	}{
		{"after a revocation", "refresh_expired_or_revoked", []auth.HealthEvent{authorized, revoked, relogin}},
		{"after a refresh since", "refresh_expired_or_revoked", []auth.HealthEvent{authorized, revoked, relogin, ev(0, auth.HealthRefreshed)}},
		{"after a logout", "", []auth.HealthEvent{authorized, ev(2, auth.HealthLogout), relogin}},
		{"first sign-in", "", []auth.HealthEvent{relogin}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := auth.ExplainSession(auth.SessionInput{Connection: "local:a", Name: "a", URL: explainURL, State: signedInState(), Found: true, Health: auth.ConnectionHealth{Events: tc.events}, KeepAlive: "24h", Now: explainNow})
			if r.State != auth.StateOK || r.Cause != nil {
				t.Fatalf("%+v", r)
			}
			switch {
			case tc.want == "" && r.PreviousCause != nil:
				t.Fatalf("%+v", r.PreviousCause)
			case tc.want != "" && (r.PreviousCause == nil || r.PreviousCause.Code != tc.want || !strings.Contains(r.PreviousCause.Message, "for 15 days")):
				t.Fatalf("%+v", r.PreviousCause)
			}
		})
	}
}

// An idle expiry with keep-alive on but no keep-alive refresh recorded means
// the daemon was not running; the message says so.
func TestExplainIdleExpiryWithoutKeepAliveRuns(t *testing.T) {
	authorized := ev(16, auth.HealthAuthorized, func(e *auth.HealthEvent) { e.RefreshToken = true })
	expired := ev(1, auth.HealthRefreshFailed, terminal("invalid_grant"), func(e *auth.HealthEvent) { e.Idle = 15 * 86400; e.Trigger = auth.TriggerStart })
	explain := func(keepAlive string, events ...auth.HealthEvent) string {
		r := auth.ExplainSession(auth.SessionInput{Connection: "local:a", Name: "a", URL: explainURL, State: failedState("invalid_grant"), Found: true, Health: auth.ConnectionHealth{Events: events}, KeepAlive: keepAlive, Now: explainNow})
		return r.Cause.Message
	}
	const daemon = "only while the daemon runs"
	if m := explain("24h", authorized, expired); !strings.Contains(m, daemon) {
		t.Fatal(m)
	}
	failing := ev(10, auth.HealthRefreshFailed, func(e *auth.HealthEvent) { e.Code, e.Trigger = "timeout", auth.TriggerKeepAlive })
	for name, m := range map[string]string{
		"keep-alive ran":  explain("24h", authorized, failing, expired),
		"keep-alive off":  explain("off", authorized, expired),
		"short idle":      explain("24h", authorized, ev(1, auth.HealthRefreshFailed, terminal("invalid_grant"), func(e *auth.HealthEvent) { e.Idle = 3600 })),
		"no history kept": explain("24h", expired),
	} {
		if strings.Contains(m, daemon) {
			t.Fatal(name, m)
		}
	}
}

// The JSON keys are the documented camelCase names (section 0 problem 8).
func TestSessionReportJSONKeys(t *testing.T) {
	r := auth.SessionReport{
		Connection: "c", State: "ok", LastRefreshAt: "x", AccessTokenExpiresAt: "x", RefreshTokenExpiresAt: "x", KeepAlive: "24h", Cause: &auth.SessionCause{Code: "c", Message: "m"}, PreviousCause: &auth.SessionCause{Code: "c", Message: "m"}, NextAction: "n",
		Events: []auth.HealthEvent{{At: 1, Kind: auth.HealthRefreshed, Trigger: "t", Code: "c", HTTPStatus: 1, Terminal: true, AccessTTL: 1, RefreshTTL: 1, RefreshToken: true, Rotated: true, Idle: 1, Interrupted: true, ReusedClient: true}},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(maps.Keys(top)); !slices.Equal(got, []string{"accessTokenExpiresAt", "cause", "connection", "events", "keepAlive", "lastRefreshAt", "nextAction", "previousCause", "refreshTokenExpiresAt", "state"}) {
		t.Fatal(got)
	}
	var events []map[string]any
	if err := json.Unmarshal(top["events"], &events); err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(maps.Keys(events[0])); !slices.Equal(got, []string{"accessTtl", "at", "code", "idle", "interrupted", "kind", "refreshToken", "refreshTtl", "reusedClient", "rotated", "status", "terminal", "trigger"}) {
		t.Fatal(got)
	}
}
