package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

type LockData struct {
	Locked bool `json:"locked"`
}

// admitProtected marks w as protected work, which auth lock cancels. Work
// admitted before a lock is refused here, before any provider call or prompt.
func (p *pool) admitProtected(w *poolWork) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if w.epoch != p.lockEpoch {
		return auth.ErrLocked
	}
	w.protected = true
	return nil
}

// resolveLease resolves a protected connection's lease; the connection's gate
// is held. A failed or expired session, or a service-account token that can
// no longer be read, also stops the connection's pooled process, so no old
// process outlives a failed revalidation. A rate limit keeps it: the provider
// refused this request only.
func (p *pool) resolveLease(ctx context.Context, canonical string, c config.Connection, profile config.Profile, refs []string, noInput bool) (auth.Lease, error) {
	lease, e := p.resolve(ctx, c, profile, refs, noInput)
	if e == nil {
		return lease, nil
	}
	if retiresProcess(e) {
		p.mu.Lock()
		entry := p.entries[canonical]
		p.mu.Unlock()
		if entry != nil {
			p.retire(canonical, entry)
		}
	}
	return lease, p.leaseError(c.CredentialProfile, profile, e)
}

// retiresProcess reports whether a failed revalidation stops the
// connection's pooled process: a failed or expired session, or a
// service-account token that can no longer be read. A rate limit does not.
func retiresProcess(e error) bool {
	return errors.Is(e, auth.ErrProvider) || errors.Is(e, auth.ErrExpired) || errors.Is(e, auth.ErrTokenUnavailable) || errors.Is(e, auth.ErrTokenUnsafe)
}

// retireAfterGate stops canonical's pooled process once the work holding its
// gate ends: a failed re-read outside the gate must not cancel a running
// call, yet no process outlives a failed revalidation.
func (p *pool) retireAfterGate(canonical string) {
	p.mu.Lock()
	entry := p.entries[canonical]
	if p.closed || entry == nil || entry.sweeping {
		p.mu.Unlock()
		return
	}
	entry.sweeping = true
	p.sweeps.Add(1)
	p.mu.Unlock()
	go func() {
		defer p.sweeps.Done()
		gate := p.gate(canonical)
		<-gate
		defer func() { gate <- struct{}{} }()
		p.retire(canonical, entry)
	}()
}

// resolve reads a protected connection's lease and logs a failure. It
// retires nothing, so it is safe without the connection's gate; its error is
// the resolver's own (see leaseError).
func (p *pool) resolve(ctx context.Context, c config.Connection, profile config.Profile, refs []string, noInput bool) (auth.Lease, error) {
	authCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	lease, e := p.opts.Credentials.Resolve(authCtx, c.CredentialProfile, profile, refs, noInput)
	if e == nil && auth.Expired(p.opts.Now(), lease.SessionExpiresAt) {
		e = auth.ErrExpired
	}
	switch {
	case e == nil:
	case errors.Is(e, auth.ErrRateLimited):
		p.opts.Log("auth_rate_limited")
	default:
		p.opts.Log("auth_failed")
	}
	return lease, e
}

// leaseError maps a service-account profile's bootstrap failure to its
// named error; any other error is returned as is.
func (p *pool) leaseError(id string, profile config.Profile, e error) error {
	if profile.PromptFree() && !errors.Is(e, auth.ErrRateLimited) {
		if safe := p.serviceAccountError(id, profile, e); safe != nil {
			return safe
		}
	}
	return e
}

// rereadSecrets is auth <mcp> for a connection whose only credentials are
// 1Password references: drop the cached values and read them again now, so
// a prompt or error shows here. It holds no gate: an in-flight call keeps
// the values it started with, and its connection reconnects on its next
// call only when a value changed. A failed re-read stops the pooled process
// once that call ends.
func (p *pool) rereadSecrets(ctx context.Context, w *poolWork, req Request, plan AuthPlan, profile config.Profile, fail func(error) Response) Response {
	if e := p.admitProtected(w); e != nil {
		return fail(e)
	}
	c := plan.Connection
	if e := p.rereadRefused(req.Connection, plan.Canonical, c.CredentialProfile, profile, req.NoInput); e != nil {
		return fail(e)
	}
	p.opts.Credentials.Invalidate(c.CredentialProfile, plan.Refs)
	p.opts.Log("credential_invalidated")
	if _, e := p.resolve(ctx, c, profile, plan.Refs, req.NoInput); e != nil {
		if retiresProcess(e) {
			p.retireAfterGate(plan.Canonical)
		}
		return fail(p.leaseError(c.CredentialProfile, profile, e))
	}
	return authResponse(AuthData{Connection: plan.Canonical, SecretsRefreshed: ptr(true)}, fail)
}

// rereadRefused refuses a re-read that could only fail, before the cache is
// dropped: a desktop-app profile where the app cannot be used (Linux, D8),
// and --no-input with a "desktop" profile, which the resolver serves from
// its cache only.
func (p *pool) rereadRefused(name, canonical, profileID string, profile config.Profile, noInput bool) error {
	switch {
	case p.opts.NoDesktopApp && profile.UsesDesktopApp():
		return output.DesktopAppUnavailableError(profileID)
	case noInput && profile.Mode == config.ProfileModeDesktop:
		return output.RereadNeedsInputError(name, canonical)
	}
	return nil
}

// serviceAccountError names a service-account profile's token source in the
// errors of its bootstrap: the provider's sentinels carry no profile. It is
// nil for any other error.
func (p *pool) serviceAccountError(id string, profile config.Profile, e error) *output.Error {
	switch {
	case errors.Is(e, auth.ErrTokenUnavailable) && profile.TokenEnv != "":
		return output.ServiceAccountTokenEnvError(id, profile.TokenEnv, p.opts.Headless)
	case errors.Is(e, auth.ErrTokenUnavailable):
		return output.ServiceAccountTokenFileError(id, profile.TokenFile)
	case errors.Is(e, auth.ErrTokenUnsafe):
		return output.ServiceAccountTokenUnsafeError(id, profile.TokenFile)
	case errors.Is(e, auth.ErrProviderBusy):
		// The token was never sent; the generic auth_failed applies.
		return nil
	case errors.Is(e, auth.ErrProvider):
		return output.ServiceAccountRejectedError(id, profile.TokenEnv, profile.TokenFile, p.opts.Headless)
	}
	return nil
}

// lock ends every credential session: protected work is canceled with cause
// auth.ErrLocked, protected processes are stopped, 1Password sessions end and
// every OAuth connection is barred from reusing its stored session until its
// next sign-in. Secret-free connections keep their processes.
func (p *pool) lock(ctx context.Context) Response {
	ids, idsErr := p.oauthLockIDs()
	// One critical section: a sign-in cannot clear the new lock entries and no
	// work with the new epoch can reach a pre-lock 1Password session. The
	// resolver never calls back into the pool.
	p.lockMu.Lock()
	set := p.oauthLocksLocked()
	for _, id := range ids {
		set[id] = true
	}
	p.mu.Lock()
	p.lockEpoch++
	for w, cancel := range p.requests {
		if w.protected {
			cancel(auth.ErrLocked)
		}
	}
	closing := map[string]*poolEntry{}
	for id, entry := range p.entries {
		if entry.identity != "public" || entry.oauth != nil {
			delete(p.entries, id)
			closing[id] = entry
		}
	}
	if p.opts.Credentials != nil {
		p.opts.Credentials.Lock()
	}
	p.mu.Unlock()
	p.lockMu.Unlock()
	var wg sync.WaitGroup
	for id, entry := range closing {
		// The first cause wins, so in-flight calls report the lock.
		entry.cancel(auth.ErrLocked)
		wg.Go(func() {
			// Let the canceled call unwind before its transport closes,
			// bounded like a shutdown.
			wait, stop := context.WithTimeout(context.WithoutCancel(ctx), p.opts.ShutdownTimeout)
			defer stop()
			gate := p.gate(id)
			select {
			case <-gate:
				defer func() { gate <- struct{}{} }()
			case <-wait.Done():
			}
			p.retire(id, entry)
		})
	}
	wg.Wait()
	p.lockMu.Lock()
	err := p.saveOAuthLocksLocked()
	p.lockMu.Unlock()
	p.opts.Log("auth_locked")
	if err = errors.Join(idsErr, err); err != nil {
		e := output.NewError("internal_error", nil)
		e.Message = "1Password sessions ended, but the OAuth lock could not be saved."
		return Response{Error: e}
	}
	b, err := json.Marshal(LockData{Locked: true})
	if err != nil {
		return Response{Error: output.NewError("internal_error", nil)}
	}
	return Response{Data: b}
}

// CredentialSessions reports the credential profile sessions for runtime
// status; never an account, reference or value.
func (p *pool) CredentialSessions() []auth.SessionInfo {
	if p.opts.Credentials == nil {
		return nil
	}
	return p.opts.Credentials.Sessions()
}

// oauthLockIDs lists the connections auth lock bars: every OAuth-capable
// effective connection, enabled or not, and every one with sign-in history.
func (p *pool) oauthLockIDs() ([]string, error) {
	var ids []string
	snapshot, err := p.opts.Load(p.opts.Paths)
	if err == nil && snapshot.Effective != nil {
		for id, row := range snapshot.Effective.Connections {
			if row.Connection != nil && oauthCapable(*row.Connection) {
				ids = append(ids, id)
			}
		}
	}
	health, herr := auth.ReadHealth(p.opts.Paths.StateDir)
	for id := range health {
		ids = append(ids, id)
	}
	return ids, errors.Join(err, herr)
}

// oauthLocked reports whether auth lock barred id's stored OAuth session.
func (p *pool) oauthLocked(id string) bool {
	p.lockMu.Lock()
	defer p.lockMu.Unlock()
	return p.oauthLocksLocked()[id]
}

// oauthLocksLocked returns the locked set, reading auth-lock.json once; the
// daemon is its only writer. An unreadable file fails closed: every
// connection auth lock would bar counts as locked. p.lockMu is held.
func (p *pool) oauthLocksLocked() map[string]bool {
	if p.locks == nil {
		set, err := auth.ReadOAuthLock(p.opts.Paths.StateDir)
		if err != nil {
			set = map[string]bool{}
			ids, _ := p.oauthLockIDs()
			for _, id := range ids {
				set[id] = true
			}
		}
		p.locks = set
	}
	return p.locks
}

// unlockOAuth clears id's lock after a sign-in completed by work admitted at
// epoch. It reports false, and keeps the lock, when an auth lock ran since.
func (p *pool) unlockOAuth(id string, epoch uint64) bool {
	p.lockMu.Lock()
	defer p.lockMu.Unlock()
	p.mu.Lock()
	current := p.lockEpoch
	p.mu.Unlock()
	if current != epoch {
		return false
	}
	if set := p.oauthLocksLocked(); set[id] {
		delete(set, id)
		_ = p.saveOAuthLocksLocked()
	}
	return true
}

// saveOAuthLocksLocked writes the locked set; p.lockMu is held.
func (p *pool) saveOAuthLocksLocked() error {
	ids := make([]string, 0, len(p.locks))
	for id := range p.locks {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return auth.WriteOAuthLock(p.opts.Paths.StateDir, ids)
}
