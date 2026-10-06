package auth

import (
	"fmt"
	"time"
)

// SessionInput is what auth status knows about one connection: its Keychain
// item (Found false when absent), its health history and, from task B, its
// keep-alive setting ("", a duration, "off" or "unavailable").
type SessionInput struct {
	Connection, Name, URL string // URL: the configured MCP URL
	State                 OAuthState
	Found                 bool
	Health                ConnectionHealth
	KeepAlive             string
	Now                   time.Time
}

// SessionCause says why a session needs sign-in or is at risk. Message is
// fixed text with sanitized codes and numbers only.
type SessionCause struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// SessionReport is auth status's explanation of one connection. Cause says why
// the session needs sign-in or is at risk; PreviousCause, for a signed-in
// session, why its last sign-in was needed.
type SessionReport struct {
	Connection            string        `json:"connection"`
	State                 string        `json:"state"` // ok | expiring | sign-in required
	LastRefreshAt         string        `json:"lastRefreshAt,omitempty"`
	AccessTokenExpiresAt  string        `json:"accessTokenExpiresAt,omitempty"`
	RefreshTokenExpiresAt string        `json:"refreshTokenExpiresAt,omitempty"`
	KeepAlive             string        `json:"keepAlive,omitempty"`
	Cause                 *SessionCause `json:"cause,omitempty"`
	PreviousCause         *SessionCause `json:"previousCause,omitempty"`
	NextAction            string        `json:"nextAction,omitempty"`
	Events                []HealthEvent `json:"events,omitempty"`
}

const (
	StateOK                   = "ok"
	StateExpiring             = "expiring"
	StateSignInRequired       = "sign-in required"
	expiringWithin            = 72 * time.Hour
	secondsPerDay       int64 = 24 * 60 * 60
)

// ExplainSession turns a stored item and its history into a state, a cause
// and a next action. It is pure: no Keychain, file or network access.
func ExplainSession(in SessionInput) SessionReport {
	r := SessionReport{Connection: in.Connection, KeepAlive: in.KeepAlive, Events: in.Health.Events}
	signedIn := in.Found && in.State.URL == in.URL && in.State.Failure == nil && in.State.RefreshToken != ""
	success := lastSuccess(in.Health.Events)
	var refreshExpiry int64
	if success != nil {
		r.LastRefreshAt = rfc3339(success.At)
		if success.RefreshTTL > 0 {
			refreshExpiry = success.At + success.RefreshTTL
			r.RefreshTokenExpiresAt = rfc3339(refreshExpiry)
		}
	}
	if in.Found && in.State.AccessExpiry != 0 {
		r.AccessTokenExpiresAt = rfc3339(in.State.AccessExpiry)
	}
	r.Cause = explainCause(in, signedIn, success)
	if signedIn {
		r.PreviousCause = previousCause(in)
	}
	switch {
	case !signedIn:
		r.State = StateSignInRequired
		r.NextAction = "mcparcel auth login " + in.Name
	case refreshExpiry != 0 && refreshExpiry-in.Now.Unix() < int64(expiringWithin/time.Second),
		r.Cause != nil && r.Cause.Code == "refresh_failing":
		r.State = StateExpiring
	default:
		r.State = StateOK
	}
	return r
}

func explainCause(in SessionInput, signedIn bool, success *HealthEvent) *SessionCause {
	events := in.Health.Events
	var last *HealthEvent
	if len(events) > 0 {
		last = &events[len(events)-1]
	}
	if signedIn {
		if last != nil && last.Kind == HealthRefreshFailed && !last.Terminal {
			return &SessionCause{"refresh_failing", fmt.Sprintf("The last refresh failed (%s); it is tried again on the next use.", sanitizeCode(last.Code, "error"))}
		}
		return nil
	}
	if !in.Found {
		logout := lastKind(events, HealthLogout)
		switch {
		case last != nil && last.Kind == HealthLogout:
			return &SessionCause{"signed_out", "Signed out with mcparcel auth logout."}
		case success != nil && (logout == nil || success.At >= logout.At):
			return &SessionCause{"keychain_missing", "The Keychain item is missing although a sign-in was recorded; it was removed outside MCParcel."}
		case len(events) == 0:
			return &SessionCause{"never_signed_in", "Not signed in yet."}
		}
	}
	if in.Found && in.State.TokenURL == "" && in.State.RefreshToken == "" && in.State.Failure == nil {
		return &SessionCause{"server_requested", "The server asked for sign-in."}
	}
	if in.Found && in.State.URL != in.URL {
		return &SessionCause{"url_changed", "The connection's URL changed since sign-in."}
	}
	if i := lastTerminal(events); i >= 0 {
		return terminalCause(in, events, i)
	}
	if in.Found && in.State.Failure != nil {
		// Recorded before the health history existed.
		return terminalCause(in, []HealthEvent{{At: in.State.Failure.At, Code: in.State.Failure.Code, Terminal: true}}, 0)
	}
	if in.Found && in.State.RefreshToken == "" {
		if a := lastKind(events, HealthAuthorized); a == nil || !a.RefreshToken {
			return &SessionCause{"no_refresh_token", "The provider issued no refresh token, so the session ended with its access token."}
		}
	}
	if !in.Found {
		return &SessionCause{"never_signed_in", "Not signed in yet."}
	}
	return nil
}

// previousCause explains the terminal failure that preceded the last sign-in,
// nil when that sign-in followed a logout or was the first.
func previousCause(in SessionInput) *SessionCause {
	events := in.Health.Events
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == HealthAuthorized {
			if j := lastTerminal(events[:i]); j >= 0 {
				return terminalCause(in, events, j)
			}
			return nil
		}
	}
	return nil
}

// terminalCause explains the terminal failure events[i].
func terminalCause(in SessionInput, events []HealthEvent, i int) *SessionCause {
	e := events[i]
	success := lastSuccess(events[:i])
	code := sanitizeCode(e.Code, "error")
	switch code {
	case "invalid_grant":
		if e.Interrupted {
			return &SessionCause{"interrupted_refresh", "A refresh was interrupted before its new refresh token was saved, so the provider rejected the stored one (invalid_grant)."}
		}
		msg := "The provider rejected the refresh token (invalid_grant): it expired or was revoked."
		switch {
		case e.Idle >= secondsPerDay:
			msg += fmt.Sprintf(" The session had not been refreshed for %d days.", e.Idle/secondsPerDay)
		case e.Idle >= 3600:
			msg += fmt.Sprintf(" The session had not been refreshed for %d hours.", e.Idle/3600)
		}
		if in.KeepAlive == "off" {
			msg += " Keep-alive was off."
		} else if success != nil && keepAliveNeverRan(in.KeepAlive, e, events[:i]) {
			msg += " Keep-alive refreshes only while the daemon runs, and none ran in that time; runtime.keepAlive keeps the daemon running."
		}
		if success != nil && success.RefreshTTL > 0 && e.At-success.At >= success.RefreshTTL {
			msg += fmt.Sprintf(" Its known lifetime of %d days had passed.", success.RefreshTTL/secondsPerDay)
		}
		return &SessionCause{"refresh_expired_or_revoked", msg}
	case "invalid_client", "unauthorized_client":
		return &SessionCause{"client_rejected", fmt.Sprintf("The provider no longer accepts this client registration (%s).", code)}
	case "issuer_changed":
		return &SessionCause{"issuer_changed", "The authorization server changed since sign-in."}
	case "token_rejected":
		return &SessionCause{"token_rejected", "The server rejected an access token right after it was refreshed."}
	case "server_requested":
		return &SessionCause{"server_requested", "The server asked for sign-in."}
	}
	return &SessionCause{"refresh_rejected", fmt.Sprintf("The provider rejected the refresh (%s).", code)}
}

// keepAliveNeverRan reports an idle expiry that keep-alive would have
// prevented had it run: the session sat idle for longer than the keep-alive
// interval plus its longest backoff, and no background refresh was recorded
// since the last success (before is the history up to the failure).
func keepAliveNeverRan(setting string, failure HealthEvent, before []HealthEvent) bool {
	if setting == "" || setting == "unavailable" {
		return false
	}
	interval, ok := KeepAliveInterval(setting)
	if !ok || failure.Idle < int64((interval+keepAliveMaxDelay)/time.Second) {
		return false
	}
	for i := len(before) - 1; i >= 0; i-- {
		e := before[i]
		if e.Kind == HealthAuthorized || e.Kind == HealthRefreshed {
			return true
		}
		if e.Trigger == TriggerKeepAlive || e.Trigger == TriggerStart {
			return false
		}
	}
	return true
}

// lastTerminal is the index of the newest terminal event not followed by a
// success, or -1.
func lastTerminal(events []HealthEvent) int {
	for i := len(events) - 1; i >= 0; i-- {
		switch {
		case events[i].Terminal:
			return i
		case events[i].Kind == HealthAuthorized, events[i].Kind == HealthRefreshed:
			return -1
		}
	}
	return -1
}

func lastKind(events []HealthEvent, kind HealthKind) *HealthEvent {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == kind {
			return &events[i]
		}
	}
	return nil
}

func rfc3339(unix int64) string { return time.Unix(unix, 0).UTC().Format(time.RFC3339) }
