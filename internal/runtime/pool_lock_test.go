package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/mcpclient"
	"github.com/dedene/mcparcel/internal/testutil"
)

func lockPool(t *testing.T, r *poolRig) {
	t.Helper()
	res := r.h.Handle(testCtx(t), testID, Request{Method: "lock", Arguments: emptyArgs()}, nil)
	success(t, res)
	var data LockData
	if e := json.Unmarshal(res.Data, &data); e != nil || !data.Locked {
		t.Fatal(string(res.Data), e)
	}
}

func TestLockDuringActiveCall(t *testing.T) {
	started := make(chan string, 1)
	release := make(chan struct{})
	defer close(release)
	r, vault, _ := vaultRig(t)
	r.http("a", testutil.FixtureOptions{Started: started, Release: release}, true)
	r.stdio("paper", false)
	vault.Set(secretRef("a"), "value")
	var mu sync.Mutex
	var events []string
	r.opts.Log = func(e string) { mu.Lock(); events = append(events, e); mu.Unlock() }
	r.start()
	count(t, r.call(testCtx(t), "paper", "counter"))
	active := asyncCall(r, testCtx(t), "a", "wait")
	<-started
	queued := asyncCall(r, testCtx(t), "a", "counter")
	awaitActive(t, r.h, 2)
	lockPool(t, r)
	responseCode(t, response(t, active), "outcome_unknown", true)
	responseCode(t, response(t, queued), "auth_required", false)
	if vault.Boots() != 1 || r.closed.Load() != 1 {
		t.Fatal("bootstrapped after the lock or kept the process", vault.Boots(), r.closed.Load())
	}
	if n := count(t, noInputCall(r, "paper")); n != 2 {
		t.Fatal("secret-free connection affected", n)
	}
	responseCode(t, noInputCall(r, "a"), "auth_required", false)
	if vault.Boots() != 1 {
		t.Fatal("--no-input bootstrapped")
	}
	if n := count(t, r.call(testCtx(t), "a", "counter")); n != 1 || vault.Boots() != 2 {
		t.Fatal("no single new bootstrap", n, vault.Boots())
	}
	mu.Lock()
	defer mu.Unlock()
	if !hasEvent(events, "auth_locked") {
		t.Fatal(events)
	}
}

// A call queued on the gate before the lock never resolves, so it can never
// open a 1Password prompt the user just locked away.
func TestLockRefusesQueuedBootstrap(t *testing.T) {
	r, vault, _ := vaultRig(t)
	r.stdio("a", true)
	vault.Set(secretRef("a"), "value")
	r.start()
	gate := r.h.(*pool).gate("local:a")
	<-gate
	queued := asyncCall(r, testCtx(t), "a", "counter")
	awaitActive(t, r.h, 1)
	lockPool(t, r)
	gate <- struct{}{}
	responseCode(t, response(t, queued), "auth_required", false)
	if vault.Boots() != 0 || r.connects.Load() != 0 {
		t.Fatal("effects after lock", vault.Boots(), r.connects.Load())
	}
}

func hasEvent(events []string, want string) bool {
	for _, e := range events {
		if e == want {
			return true
		}
	}
	return false
}

func TestAuthLockBlocksStoredTokenReuse(t *testing.T) {
	r, _, _ := oauthRig(t, testutil.AuthServerOptions{ClientID: "pre-id"}, true)
	r.start()
	b := &loginBrowser{visit: true}
	success(t, r.login(testCtx(t), b, false))
	b.wg.Wait()
	count(t, r.call(testCtx(t), "a", "counter"))
	lockPool(t, r)
	if r.closed.Load() != 1 {
		t.Fatal("signed-in session kept", r.closed.Load())
	}
	signInAction(t, r.call(testCtx(t), "a", "counter"))
	if set, e := auth.ReadOAuthLock(r.paths.StateDir); e != nil || !set["local:a"] {
		t.Fatal(set, e)
	}
	// A new daemon reads the lock from disk.
	if e := r.h.Shutdown(testCtx(t), true); e != nil {
		t.Fatal(e)
	}
	r.h = NewPool(r.opts)
	signInAction(t, r.call(testCtx(t), "a", "counter"))
	if r.connects.Load() != 1 {
		t.Fatal("connected with a locked session", r.connects.Load())
	}
}

func TestLoginClearsLock(t *testing.T) {
	r, _, _ := oauthRig(t, testutil.AuthServerOptions{ClientID: "pre-id"}, true)
	r.start()
	lockPool(t, r)
	if set, _ := auth.ReadOAuthLock(r.paths.StateDir); !set["local:a"] {
		t.Fatal("not locked", set)
	}
	b := &loginBrowser{visit: true}
	success(t, r.login(testCtx(t), b, false))
	b.wg.Wait()
	count(t, r.call(testCtx(t), "a", "counter"))
	if set, e := auth.ReadOAuthLock(r.paths.StateDir); e != nil || set["local:a"] {
		t.Fatal("lock kept after sign-in", set, e)
	}
}

func TestKeepAliveSkipsLocked(t *testing.T) {
	k := newKeepAliveRig(t, testutil.AuthServerOptions{ClientID: "pre-id"}, true)
	k.storeGrant(t)
	k.start()
	before := k.refreshes()
	lockPool(t, k.poolRig)
	k.clock.Advance(25 * time.Hour)
	k.sweep(t, auth.TriggerKeepAlive)
	if k.refreshes() != before {
		t.Fatal("refreshed a locked session", k.refreshes()-before)
	}
	if e := k.pool().refreshStored(testCtx(t), "local:a", auth.TriggerKeepAlive); e != auth.ErrKeepAliveDormant {
		t.Fatal(e)
	}
}

func TestStatusCredentialSessions(t *testing.T) {
	r, vault, _ := vaultRig(t)
	r.stdio("a", true)
	vault.Set(secretRef("a"), "VALUE-CANARY")
	r.start()
	count(t, r.call(testCtx(t), "a", "counter"))
	c, _ := service(t, r.h, time.Minute)
	s := waitStatus(t, c)
	if len(s.CredentialSessions) != 1 || s.CredentialSessions[0].Profile != "shared" || s.CredentialSessions[0].Mode != "desktop-service-account" || s.CredentialSessions[0].State != "active" || s.CredentialSessions[0].ExpiresAt.IsZero() {
		t.Fatalf("%+v", s.CredentialSessions)
	}
	b, _ := json.Marshal(s)
	for _, leak := range []string{"VALUE-CANARY", "op://", `"account"`, "Bootstrap"} {
		if strings.Contains(string(b), leak) {
			t.Fatal("status leaks", leak)
		}
	}
}

// pausedLock holds Credentials.Lock until release closes.
type pausedLock struct {
	auth.Resolver
	entered, release chan struct{}
}

func (p pausedLock) Lock() {
	close(p.entered)
	<-p.release
	p.Resolver.Lock()
}

// Regression: work admitted between the epoch bump and Credentials.Lock was
// served from the pre-lock 1Password session and its process outlived the lock.
func TestLockEndsSessionsBeforeNewWork(t *testing.T) {
	r, vault, _ := vaultRig(t)
	paused := pausedLock{Resolver: r.opts.Credentials, entered: make(chan struct{}), release: make(chan struct{})}
	r.opts.Credentials = paused
	r.stdio("a", true)
	vault.Set(secretRef("a"), "value")
	r.start()
	count(t, r.call(testCtx(t), "a", "counter"))
	locked := make(chan Response, 1)
	go func() {
		locked <- r.h.Handle(testCtx(t), testID, Request{Method: "lock", Arguments: emptyArgs()}, nil)
	}()
	<-paused.entered
	during := asyncCall(r, testCtx(t), "a", "counter")
	// Time for a call that is not held back to finish on the old session.
	time.Sleep(50 * time.Millisecond)
	close(paused.release)
	success(t, response(t, locked))
	if n := count(t, response(t, during)); n != 1 || vault.Boots() != 2 {
		t.Fatal("served from the pre-lock session", n, vault.Boots())
	}
}

// Regression: a login that completed while auth lock ran cleared the new lock.
func TestLockDuringLoginKeepsLock(t *testing.T) {
	r, _, _ := oauthRig(t, testutil.AuthServerOptions{ClientID: "pre-id"}, true)
	r.start()
	p := r.h.(*pool)
	p.mu.Lock()
	epoch := p.lockEpoch
	p.mu.Unlock()
	lockPool(t, r)
	if p.unlockOAuth("local:a", epoch) {
		t.Fatal("a login admitted before the lock cleared it")
	}
	if set, e := auth.ReadOAuthLock(r.paths.StateDir); e != nil || !set["local:a"] {
		t.Fatal(set, e)
	}
	p.mu.Lock()
	epoch = p.lockEpoch
	p.mu.Unlock()
	if !p.unlockOAuth("local:a", epoch) {
		t.Fatal("a login after the lock did not clear it")
	}
}

// A protected process that finishes connecting after auth lock is closed, not
// pooled, even when its connect ignored the cancellation.
func TestLockClosesProcessConnectingDuringLock(t *testing.T) {
	r, vault, _ := vaultRig(t)
	r.stdio("a", true)
	vault.Set(secretRef("a"), "value")
	var mu sync.Mutex
	var events []string
	r.opts.Log = func(e string) { mu.Lock(); events = append(events, e); mu.Unlock() }
	entered, release := make(chan struct{}), make(chan struct{})
	connect := r.opts.Connect
	r.opts.Connect = func(ctx context.Context, o mcpclient.ConnectOptions) (mcpclient.Session, error) {
		close(entered)
		<-release
		return connect(context.WithoutCancel(ctx), o)
	}
	r.start()
	call := asyncCall(r, testCtx(t), "a", "counter")
	<-entered
	lockPool(t, r)
	close(release)
	responseCode(t, response(t, call), "auth_required", false)
	awaitClosed(t, r, 1)
	p := r.h.(*pool)
	p.mu.Lock()
	pooled := len(p.entries)
	p.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	if pooled != 0 || hasEvent(events, "connection_opened") {
		t.Fatal("pooled a process that connected during the lock", pooled, events)
	}
}

// An unreadable auth-lock.json fails closed: no stored OAuth session is used
// or kept alive.
func TestUnreadableLockFileLocksAll(t *testing.T) {
	k := newKeepAliveRig(t, testutil.AuthServerOptions{ClientID: "pre-id"}, true)
	k.storeGrant(t)
	if e := os.MkdirAll(k.paths.StateDir, 0o700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(k.paths.StateDir, "auth-lock.json"), []byte("{"), 0o600); e != nil {
		t.Fatal(e)
	}
	k.start()
	before := k.refreshes()
	signInAction(t, k.call(testCtx(t), "a", "counter"))
	k.clock.Advance(25 * time.Hour)
	k.sweep(t, auth.TriggerKeepAlive)
	if k.refreshes() != before || k.connects.Load() != 0 {
		t.Fatal("used a session behind an unreadable lock file", k.refreshes()-before, k.connects.Load())
	}
}
