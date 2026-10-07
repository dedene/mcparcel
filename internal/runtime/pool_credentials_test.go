package runtime

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

// vaultRig serves the protected connections' secrets from a fake vault on a
// fake clock shared by the pool and the resolver.
func vaultRig(t *testing.T) (*poolRig, *testutil.FakeVault, *testutil.Clock) {
	t.Helper()
	r := newRig(t)
	vault, clock := &testutil.FakeVault{}, testutil.NewClock()
	r.opts.Now = clock.Now
	r.opts.Credentials = auth.NewResolver(auth.ResolverOptions{Now: clock.Now, Provider: vault})
	return r, vault, clock
}

func secretRef(id string) string { return "op://vault/" + id + "/value" }

func noInputCall(r *poolRig, id string) Response {
	return r.h.Handle(testCtx(r.t), testID, Request{Method: "call", Connection: id, Tool: "counter", NoInput: true, Arguments: emptyArgs()}, nil)
}

func connectSecret(t *testing.T, r *poolRig) string {
	t.Helper()
	select {
	case o := <-r.captured:
		if o.Connection.Transport.HTTP != nil {
			return o.Headers["X-Secret"]
		}
		return o.Env["SECRET"]
	case <-time.After(5 * time.Second):
		t.Fatal("no connect")
		return ""
	}
}

func TestUnrelatedSecretChangeNoReconnect(t *testing.T) {
	r, vault, clock := vaultRig(t)
	r.stdio("a", true)
	r.stdio("b", true)
	vault.Set(secretRef("a"), "a1")
	vault.Set(secretRef("b"), "b1")
	r.start()
	count(t, r.call(testCtx(t), "a", "counter"))
	count(t, r.call(testCtx(t), "b", "counter"))
	vault.Set(secretRef("b"), "b2")
	clock.Advance(5 * time.Minute)
	if n := count(t, r.call(testCtx(t), "a", "counter")); n != 2 || vault.Reads(secretRef("a")) != 2 {
		t.Fatal("a reconnected or was not re-read", n, vault.Reads(secretRef("a")))
	}
	if n := count(t, r.call(testCtx(t), "b", "counter")); n != 1 {
		t.Fatal("b kept its old value", n)
	}
	if r.connects.Load() != 3 || r.closed.Load() != 1 || vault.Boots() != 1 {
		t.Fatal(r.connects.Load(), r.closed.Load(), vault.Boots())
	}
}

func TestRotationDrainsActiveCall(t *testing.T) {
	started := make(chan string, 1)
	release := make(chan struct{})
	r, vault, clock := vaultRig(t)
	r.http("a", testutil.FixtureOptions{Started: started, Release: release}, true)
	vault.Set(secretRef("a"), "old")
	r.start()
	a := asyncCall(r, testCtx(t), "a", "wait")
	<-started
	vault.Set(secretRef("a"), "new")
	clock.Advance(5 * time.Minute)
	b := asyncCall(r, testCtx(t), "a", "counter")
	awaitActive(t, r.h, 2)
	if r.connects.Load() != 1 || r.closed.Load() != 0 || vault.Reads(secretRef("a")) != 1 {
		t.Fatal("rotated while a call was active")
	}
	close(release)
	success(t, response(t, a))
	if n := count(t, response(t, b)); n != 1 {
		t.Fatal("reused the old process", n)
	}
	if first, second := connectSecret(t, r), connectSecret(t, r); first != "old" || second != "new" || r.closed.Load() != 1 {
		t.Fatal("bad rotation", first, second, r.closed.Load())
	}
}

func TestExpiryAfterSleepRetiresSession(t *testing.T) {
	r, vault, clock := vaultRig(t)
	r.opts.ExpiryCheck = 10 * time.Millisecond
	r.stdio("a", true)
	vault.Set(secretRef("a"), "value")
	r.start()
	count(t, r.call(testCtx(t), "a", "counter"))
	// A sleep moves the wall clock past the 24-hour deadline while the
	// monotonic clock stands still; no call is needed to end the session.
	clock.AdvanceWall(25 * time.Hour)
	awaitClosed(t, r, 1)
	responseCode(t, noInputCall(r, "a"), "auth_expired", false)
	if vault.Boots() != 1 {
		t.Fatal("bootstrapped without input")
	}
}

// Regression: a wall clock stepped back ended the 1Password session, but the
// pool kept its process, and the secrets in it, running.
func TestWallClockStepBackRetiresSession(t *testing.T) {
	r, vault, clock := vaultRig(t)
	r.opts.ExpiryCheck = 10 * time.Millisecond
	r.stdio("a", true)
	vault.Set(secretRef("a"), "value")
	r.start()
	count(t, r.call(testCtx(t), "a", "counter"))
	clock.AdvanceWall(-time.Hour)
	awaitClosed(t, r, 1)
	responseCode(t, noInputCall(r, "a"), "auth_expired", false)
}

func TestSecretFreeAfterProfileExpiry(t *testing.T) {
	r, vault, clock := vaultRig(t)
	r.opts.ExpiryCheck = 10 * time.Millisecond
	r.stdio("a", true)
	r.stdio("paper", false)
	vault.Set(secretRef("a"), "value")
	r.start()
	count(t, r.call(testCtx(t), "a", "counter"))
	count(t, r.call(testCtx(t), "paper", "counter"))
	clock.Advance(24 * time.Hour)
	awaitClosed(t, r, 1)
	if n := count(t, noInputCall(r, "paper")); n != 2 || r.connects.Load() != 2 {
		t.Fatal("secret-free connection affected", n, r.connects.Load())
	}
	responseCode(t, noInputCall(r, "a"), "auth_expired", false)
}

func TestRevokedServiceAccountRetiresConnection(t *testing.T) {
	r, vault, clock := vaultRig(t)
	r.stdio("a", true)
	vault.Set(secretRef("a"), "value")
	r.start()
	count(t, r.call(testCtx(t), "a", "counter"))
	vault.Fail(secretRef("a"), errors.New("service account REVOKED-CANARY"))
	clock.Advance(5 * time.Minute)
	res := r.call(testCtx(t), "a", "counter")
	responseCode(t, res, "auth_failed", false)
	if b, _ := json.Marshal(res); strings.Contains(string(b), "REVOKED-CANARY") {
		t.Fatal("provider text leaked")
	}
	if r.closed.Load() != 1 || vault.Boots() != 1 {
		t.Fatal("old process kept or retried", r.closed.Load(), vault.Boots())
	}
	vault.Fail(secretRef("a"), nil)
	responseCode(t, noInputCall(r, "a"), "auth_required", false)
	if n := count(t, r.call(testCtx(t), "a", "counter")); n != 1 || vault.Boots() != 2 {
		t.Fatal("no single new bootstrap", n, vault.Boots())
	}
}

func TestRateLimitedCallKeepsProcess(t *testing.T) {
	r, vault, clock := vaultRig(t)
	r.stdio("a", true)
	vault.Set(secretRef("a"), "value")
	r.start()
	count(t, r.call(testCtx(t), "a", "counter"))
	vault.Fail(secretRef("a"), auth.ErrRateLimited)
	clock.Advance(5 * time.Minute)
	responseCode(t, r.call(testCtx(t), "a", "counter"), "auth_rate_limited", false)
	if r.closed.Load() != 0 {
		t.Fatal("rate limit stopped the process")
	}
	vault.Fail(secretRef("a"), nil)
	if n := count(t, noInputCall(r, "a")); n != 2 || vault.Boots() != 1 {
		t.Fatal("session or process lost", n, vault.Boots())
	}
}

func refreshReq(id string) Request {
	return Request{Method: "refresh", Connection: id, Arguments: emptyArgs()}
}

func TestRefreshInvalidatesLease(t *testing.T) {
	r, vault, _ := vaultRig(t)
	r.stdio("a", true)
	vault.Set(secretRef("a"), "one")
	r.start()
	count(t, r.call(testCtx(t), "a", "counter"))
	res := r.h.Handle(testCtx(t), testID, refreshReq("local:a"), nil)
	success(t, res)
	var data RefreshData
	if e := json.Unmarshal(res.Data, &data); e != nil || data != (RefreshData{Connection: "local:a", Invalidated: true}) {
		t.Fatal(string(res.Data), e)
	}
	if n := count(t, noInputCall(r, "a")); n != 2 || vault.Reads(secretRef("a")) != 2 {
		t.Fatal("unchanged value reconnected or was not re-read", n)
	}
	vault.Set(secretRef("a"), "two")
	success(t, r.h.Handle(testCtx(t), testID, refreshReq("local:a"), nil))
	if n := count(t, noInputCall(r, "a")); n != 1 || vault.Boots() != 1 {
		t.Fatal("changed value did not reconnect", n, vault.Boots())
	}
	if first, second := connectSecret(t, r), connectSecret(t, r); first != "one" || second != "two" {
		t.Fatal(first, second)
	}
}

func TestRefreshRequiresSecretConnection(t *testing.T) {
	r := newRig(t)
	r.http("web", testutil.FixtureOptions{}, false)
	r.stdio("envonly", false)
	c := r.personal.Connections["envonly"]
	c.Transport.Stdio.Env["TOKEN"] = config.Value{Secret: &config.SecretRef{Secret: "env:FIXTURE_TOKEN"}}
	r.personal.Connections["envonly"] = c
	r.start()
	for id, action := range map[string]string{"local:web": "", "local:envonly": "mcparcel runtime restart"} {
		res := r.h.Handle(testCtx(t), testID, refreshReq(id), nil)
		responseCode(t, res, "invalid_arguments", false)
		if res.Error.Message != id+" uses no 1Password secrets." || res.Error.NextAction != action {
			t.Fatal(res.Error.Message, res.Error.NextAction)
		}
	}
	responseCode(t, r.h.Handle(testCtx(t), testID, refreshReq("local:missing"), nil), "connection_unavailable", false)
	if r.connects.Load() != 0 {
		t.Fatal("refresh connected")
	}
}
