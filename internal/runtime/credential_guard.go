package runtime

import (
	"context"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// credentialGuard refuses the profiles a runtime cannot serve before the inner
// resolver runs, so --no-input cannot answer auth_required for a profile that
// would never work here (D8).
type credentialGuard struct {
	inner  auth.Resolver
	refuse func(id string, p config.Profile) *output.Error
}

// guardCredentials wraps inner: Resolve returns refuse's error, when non-nil,
// without calling inner. Everything else is inner's.
func guardCredentials(inner auth.Resolver, refuse func(id string, p config.Profile) *output.Error) auth.Resolver {
	return credentialGuard{inner: inner, refuse: refuse}
}

func (g credentialGuard) Resolve(ctx context.Context, id string, p config.Profile, refs []string, noInput bool) (auth.Lease, error) {
	if err := g.refuse(id, p); err != nil {
		return auth.Lease{}, err
	}
	return g.inner.Resolve(ctx, id, p, refs, noInput)
}

func (g credentialGuard) Invalidate(id string, refs []string) { g.inner.Invalidate(id, refs) }

func (g credentialGuard) Lock() { g.inner.Lock() }

func (g credentialGuard) Sessions() []auth.SessionInfo { return g.inner.Sessions() }

func (g credentialGuard) Close() error { return g.inner.Close() }

// headlessCredentials is a headless pool's resolver: inner behind the
// headless guard, or headlessResolver when there is none.
func headlessCredentials(inner auth.Resolver) auth.Resolver {
	if inner == nil {
		return headlessResolver{}
	}
	return guardCredentials(inner, headlessProfiles)
}

// headlessProfiles refuses every profile but a service-account one: headless
// mode never uses the 1Password desktop app.
func headlessProfiles(_ string, p config.Profile) *output.Error {
	if p.PromptFree() {
		return nil
	}
	return output.HeadlessOnePasswordError()
}
