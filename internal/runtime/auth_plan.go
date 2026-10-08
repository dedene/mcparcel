package runtime

import (
	"encoding/json"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// AuthPlan is what auth <mcp> does for a connection, in order: read its
// 1Password references again, then sign in or get a new client_credentials
// token.
type AuthPlan struct {
	Canonical         string
	Connection        config.Connection
	Refs              []string // op:// references (config.SecretRefs)
	SignIn            bool     // an authorization-code sign-in in the browser
	ClientCredentials bool
}

// AuthData is auth <mcp>'s result. A key is present only when its step
// applies to the connection; the steps run in field order.
type AuthData struct {
	Connection       string `json:"connection"`
	SecretsRefreshed *bool  `json:"secretsRefreshed,omitempty"`
	SignedIn         *bool  `json:"signedIn,omitempty"`
	TokenRenewed     *bool  `json:"tokenRenewed,omitempty"`
}

// PlanAuth resolves name to a runtime connection and its plan. The CLI checks
// it offline and the daemon again, so the answer does not depend on whether
// the daemon runs. A connection with nothing to authenticate is
// invalid_arguments.
func PlanAuth(snapshot config.Snapshot, name string) (AuthPlan, error) {
	canonical, c, err := snapshot.RuntimeConnection(name)
	if err != nil {
		return AuthPlan{}, err
	}
	if !HasAuthTarget(c) {
		return AuthPlan{}, noAuthTarget(name, c)
	}
	plan := AuthPlan{Canonical: canonical, Connection: c, Refs: config.SecretRefs(c), ClientCredentials: clientCredentials(c)}
	plan.SignIn = PlansSignIn(c)
	return plan, nil
}

// PlansSignIn reports whether auth <mcp> signs in to c in the browser: an
// OAuth-capable connection that does not use client_credentials. Headless
// mode and --no-input refuse such a plan, op:// references or not.
func PlansSignIn(c config.Connection) bool {
	return oauthCapable(c) && !clientCredentials(c)
}

// HasAuthTarget reports whether auth <mcp> has something to do for c:
// 1Password references, a sign-in or a client_credentials token.
func HasAuthTarget(c config.Connection) bool {
	return len(config.SecretRefs(c)) > 0 || oauthCapable(c)
}

// noAuthTarget is invalid_arguments for a connection with only literal
// values and env: references: there is nothing auth <mcp> can renew.
func noAuthTarget(name string, c config.Connection) *output.Error {
	e := output.NewError("invalid_arguments", nil)
	e.Message = name + " has no sign-in, client credentials or 1Password secrets to refresh."
	e.NextAction = ""
	if len(config.EnvRefs(c)) > 0 {
		e.Message += " Its env: values are read when the runtime starts."
		e.NextAction = "mcparcel runtime restart"
	}
	return e
}

// beginAuth runs auth <mcp>'s steps before the lease, with the connection's
// gate held and after admitProtected: it checks that the reloaded config
// still plans the same steps, drops the cached 1Password values so the lease
// reads them again, and retires a client_credentials entry, whose token dies
// with it, so the new session mints a new one.
func (p *pool) beginAuth(req Request, plan AuthPlan, snapshot config.Snapshot) error {
	again, e := PlanAuth(snapshot, plan.Canonical)
	if e != nil {
		return e
	}
	if again.SignIn != plan.SignIn || again.ClientCredentials != plan.ClientCredentials {
		return output.NewError("config_changed", nil)
	}
	c := again.Connection
	if len(again.Refs) > 0 {
		profile := snapshot.Local.CredentialProfiles[c.CredentialProfile]
		if e = p.rereadRefused(req.Connection, plan.Canonical, c.CredentialProfile, profile, req.NoInput); e != nil {
			return e
		}
		p.opts.Credentials.Invalidate(c.CredentialProfile, again.Refs)
		p.opts.Log("credential_invalidated")
	}
	if plan.ClientCredentials {
		p.mu.Lock()
		old := p.entries[plan.Canonical]
		p.mu.Unlock()
		if old != nil {
			p.retire(plan.Canonical, old)
		}
	}
	return nil
}

// authResponse marshals auth <mcp>'s result.
func authResponse(data AuthData, fail func(error) Response) Response {
	b, e := json.Marshal(data)
	if e != nil {
		return fail(e)
	}
	return Response{Data: b}
}

// stepDone is a result key: true when the step ran, absent otherwise.
func stepDone(ran bool) *bool {
	if !ran {
		return nil
	}
	return ptr(true)
}

func ptr[T any](v T) *T { return &v }
