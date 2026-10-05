package auth_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

var profile = config.Profile{Mode: "desktop-service-account", Account: "fixture", BootstrapRef: "op://fixture/bootstrap/token", SessionDuration: "24h"}

type answer struct {
	lease auth.Lease
	err   error
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return c
}

func resolver(t *testing.T, o auth.ResolverOptions) auth.Resolver {
	t.Helper()
	r := auth.NewResolver(o)
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func client(value string) auth.SecretClient {
	return testutil.FakeSecretClient{ResolveFunc: func(context.Context, string) (string, error) { return value, nil }}
}

func resolve(t *testing.T, r auth.Resolver, refs []string, noInput bool) auth.Lease {
	t.Helper()
	l, e := r.Resolve(testContext(t), "p", profile, refs, noInput)
	if e != nil {
		t.Fatal(e)
	}
	return l
}

func receive(t *testing.T, ch <-chan answer) answer {
	t.Helper()
	select {
	case a := <-ch:
		return a
	case <-testContext(t).Done():
		t.Fatal("waiter did not finish")
		return answer{}
	}
}

func TestSingleBootstrapAcrossCallers(t *testing.T) {
	var boots, reads atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	r := resolver(t, auth.ResolverOptions{Provider: testutil.FakeProvider{BootstrapFunc: func(c context.Context, _ config.Profile) (auth.SecretClient, error) {
		boots.Add(1)
		close(started)
		select {
		case <-release:
		case <-c.Done():
			return nil, c.Err()
		}
		return testutil.FakeSecretClient{ResolveFunc: func(context.Context, string) (string, error) { reads.Add(1); return "value", nil }}, nil
	}}})
	out := make(chan answer, 20)
	barrier := make(chan struct{})
	for range 20 {
		go func() {
			<-barrier
			l, e := r.Resolve(testContext(t), "p", profile, []string{"a"}, false)
			out <- answer{l, e}
		}()
	}
	close(barrier)
	<-started
	close(release)
	var identity string
	leases := []auth.Lease{}
	for range 20 {
		a := receive(t, out)
		if a.err != nil {
			t.Fatal(a.err)
		}
		if identity == "" {
			identity = a.lease.Identity
		}
		if a.lease.Identity != identity || identity == "" {
			t.Fatal("different identity")
		}
		leases = append(leases, a.lease)
	}
	leases[0].Values["a"] = "mutated"
	for _, l := range leases[1:] {
		if l.Values["a"] != "value" {
			t.Fatal("shared values map")
		}
	}
	if boots.Load() != 1 || reads.Load() != 1 {
		t.Fatalf("bootstrap/resolve counts %d/%d", boots.Load(), reads.Load())
	}
}

func TestOnlyRequestedReferences(t *testing.T) {
	var requested []string
	r := resolver(t, auth.ResolverOptions{Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) {
		return testutil.FakeSecretClient{ResolveFunc: func(_ context.Context, ref string) (string, error) {
			requested = append(requested, ref)
			return "value", nil
		}}, nil
	}}})
	a := resolve(t, r, []string{"a", "a"}, false)
	b := resolve(t, r, []string{"b"}, false)
	if !reflect.DeepEqual(requested, []string{"a", "b"}) || len(a.Values) != 1 || len(b.Values) != 1 {
		t.Fatal("wrong references")
	}
}

func TestCancelOneWaiter(t *testing.T) {
	entered := make(chan struct{}, 10)
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	r := resolver(t, auth.ResolverOptions{Now: func() time.Time { entered <- struct{}{}; return time.Now() }, Provider: testutil.FakeProvider{BootstrapFunc: func(c context.Context, _ config.Profile) (auth.SecretClient, error) {
		started <- c
		select {
		case <-release:
			return client("v"), nil
		case <-c.Done():
			return nil, c.Err()
		}
	}}})
	c, cancel := context.WithCancel(testContext(t))
	a, b := make(chan answer, 1), make(chan answer, 1)
	go func() { l, e := r.Resolve(c, "p", profile, []string{"a"}, false); a <- answer{l, e} }()
	worker := <-started
	go func() { l, e := r.Resolve(testContext(t), "p", profile, []string{"a"}, false); b <- answer{l, e} }()
	<-entered
	<-entered
	cancel()
	if !errors.Is(receive(t, a).err, context.Canceled) {
		t.Fatal("first not canceled")
	}
	if worker.Err() != nil {
		t.Fatal("second waiter lost worker")
	}
	close(release)
	if receive(t, b).err != nil {
		t.Fatal("second failed")
	}
}

func TestCancelAllWaiters(t *testing.T) {
	started := make(chan context.Context, 1)
	returned := make(chan struct{})
	r := resolver(t, auth.ResolverOptions{Provider: testutil.FakeProvider{BootstrapFunc: func(c context.Context, _ config.Profile) (auth.SecretClient, error) {
		started <- c
		<-c.Done()
		close(returned)
		return client("late"), nil
	}}})
	c, cancel := context.WithCancel(testContext(t))
	out := make(chan answer, 1)
	go func() { l, e := r.Resolve(c, "p", profile, nil, false); out <- answer{l, e} }()
	worker := <-started
	cancel()
	if !errors.Is(receive(t, out).err, context.Canceled) {
		t.Fatal("not canceled")
	}
	select {
	case <-worker.Done():
	case <-testContext(t).Done():
		t.Fatal("worker not canceled")
	}
	<-returned
	waitNoSession(t, r)
}

func waitNoSession(t *testing.T, r auth.Resolver) {
	t.Helper()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	c := testContext(t)
	for {
		_, e := r.Resolve(c, "p", profile, nil, true)
		if errors.Is(e, auth.ErrRequired) {
			return
		}
		if !errors.Is(e, auth.ErrProvider) {
			t.Fatalf("late session/error: %v", e)
		}
		select {
		case <-tick.C:
		case <-c.Done():
			t.Fatal("quarantine did not clear")
		}
	}
}

func TestNoInputNoPrompt(t *testing.T) {
	var boots atomic.Int64
	r := resolver(t, auth.ResolverOptions{Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) {
		boots.Add(1)
		return client("v"), nil
	}}})
	_, e := r.Resolve(testContext(t), "p", profile, []string{"a"}, true)
	if !errors.Is(e, auth.ErrRequired) || boots.Load() != 0 {
		t.Fatal("no input prompted")
	}
	resolve(t, r, []string{"a"}, false)
	resolve(t, r, []string{"b"}, true)
	if boots.Load() != 1 {
		t.Fatal("rebootstrap")
	}
}

type clock struct {
	offset atomic.Int64
	base   time.Time
}

func (c *clock) now() time.Time      { return c.base.Add(time.Duration(c.offset.Load())) }
func (c *clock) set(d time.Duration) { c.offset.Store(int64(d)) }
func TestFiveMinuteLease(t *testing.T) {
	clock := &clock{base: time.Now()}
	var reads atomic.Int64
	var changed atomic.Bool
	r := resolver(t, auth.ResolverOptions{Now: clock.now, LeaseDuration: time.Hour, Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) {
		return testutil.FakeSecretClient{ResolveFunc: func(context.Context, string) (string, error) {
			reads.Add(1)
			if changed.Load() {
				return "changed", nil
			}
			return "value", nil
		}}, nil
	}}})
	a := resolve(t, r, []string{"a"}, false)
	if !a.ExpiresAt.Equal(clock.base.Add(5 * time.Minute)) {
		t.Fatal("lease not capped")
	}
	clock.set(4*time.Minute + 59*time.Second)
	resolve(t, r, []string{"a"}, true)
	if reads.Load() != 1 {
		t.Fatal("early refresh")
	}
	clock.set(5 * time.Minute)
	b := resolve(t, r, []string{"a"}, true)
	if reads.Load() != 2 || a.Identity != b.Identity {
		t.Fatal("unchanged value identity")
	}
	changed.Store(true)
	clock.set(10 * time.Minute)
	d := resolve(t, r, []string{"a"}, true)
	if d.Identity == a.Identity || reads.Load() != 3 {
		t.Fatal("changed identity not advanced")
	}
}

func TestSessionDeadlineDoesNotSlide(t *testing.T) {
	clock := &clock{base: time.Now()}
	r := resolver(t, auth.ResolverOptions{Now: clock.now, Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) { return client("v"), nil }}})
	a := resolve(t, r, []string{"a"}, false)
	clock.set(23 * time.Hour)
	b := resolve(t, r, []string{"a"}, true)
	if !a.SessionExpiresAt.Equal(clock.base.Add(24*time.Hour)) || !b.SessionExpiresAt.Equal(a.SessionExpiresAt) {
		t.Fatal("sliding session")
	}
}

func TestExpiryAndClockRollback(t *testing.T) {
	for _, d := range []time.Duration{24 * time.Hour, -time.Second} {
		t.Run(d.String(), func(t *testing.T) {
			clock := &clock{base: time.Now().Round(0)}
			r := resolver(t, auth.ResolverOptions{Now: clock.now, Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) { return client("v"), nil }}})
			resolve(t, r, []string{"a"}, false)
			clock.set(d)
			_, e := r.Resolve(testContext(t), "p", profile, []string{"a"}, true)
			if !errors.Is(e, auth.ErrExpired) {
				t.Fatal("session not expired")
			}
		})
	}
}

func TestProviderErrorRedaction(t *testing.T) {
	for _, phase := range []string{"bootstrap", "resolve"} {
		t.Run(phase, func(t *testing.T) {
			failure := errors.New("BOOTSTRAP-SENTINEL secret https://private.invalid")
			r := resolver(t, auth.ResolverOptions{Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) {
				if phase == "bootstrap" {
					return nil, failure
				}
				return testutil.FakeSecretClient{ResolveFunc: func(context.Context, string) (string, error) { return "partial", failure }}, nil
			}}})
			l, e := r.Resolve(testContext(t), "p", profile, []string{"a"}, false)
			if !errors.Is(e, auth.ErrProvider) || len(l.Values) != 0 {
				t.Fatal("provider failure not cleared")
			}
			for _, s := range []string{"BOOTSTRAP-SENTINEL", "secret", "https://"} {
				if strings.Contains(e.Error(), s) {
					t.Fatal("unsafe error")
				}
			}
		})
	}
}

func TestProviderWatchdog(t *testing.T) {
	for _, phase := range []string{"bootstrap", "resolve"} {
		t.Run(phase, func(t *testing.T) {
			var boots, reads atomic.Int64
			release := make(chan struct{})
			defer close(release)
			r := resolver(t, auth.ResolverOptions{AuthTimeout: 50 * time.Millisecond, Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) {
				boots.Add(1)
				if phase == "bootstrap" {
					<-release
				}
				return testutil.FakeSecretClient{ResolveFunc: func(context.Context, string) (string, error) {
					reads.Add(1)
					if phase == "resolve" {
						<-release
					}
					return "late", nil
				}}, nil
			}}})
			start := time.Now()
			_, e := r.Resolve(testContext(t), "p", profile, []string{"a"}, false)
			if e == nil || time.Since(start) > time.Second {
				t.Fatal("watchdog failed")
			}
			for range 20 {
				_, e = r.Resolve(testContext(t), "p", profile, []string{"a"}, false)
				if !errors.Is(e, auth.ErrProvider) {
					t.Fatal("not quarantined")
				}
			}
			if boots.Load() != 1 || reads.Load() > 1 {
				t.Fatal("unbounded workers")
			}
			if e := r.Close(); e != nil {
				t.Fatal(e)
			}
			_, e = r.Resolve(testContext(t), "p", profile, nil, false)
			if !errors.Is(e, auth.ErrProvider) {
				t.Fatal("closed resolver restarted")
			}
		})
	}
}

func TestIndependentProfiles(t *testing.T) {
	started := make(chan struct{})
	r := resolver(t, auth.ResolverOptions{Provider: testutil.FakeProvider{BootstrapFunc: func(c context.Context, p config.Profile) (auth.SecretClient, error) {
		if p.Account == "blocked" {
			close(started)
			<-c.Done()
			return nil, c.Err()
		}
		return client("v"), nil
	}}})
	blocked := profile
	blocked.Account = "blocked"
	c, cancel := context.WithCancel(testContext(t))
	defer cancel()
	out := make(chan answer, 1)
	go func() { l, e := r.Resolve(c, "a", blocked, nil, false); out <- answer{l, e} }()
	<-started
	l, e := r.Resolve(testContext(t), "b", profile, []string{"a"}, false)
	if e != nil || l.Values["a"] != "v" {
		t.Fatal("unrelated profile blocked")
	}
	cancel()
	receive(t, out)
}

func TestConcurrentReferenceDedup(t *testing.T) {
	var reads atomic.Int64
	started, release := make(chan struct{}), make(chan struct{})
	r := resolver(t, auth.ResolverOptions{Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) {
		return testutil.FakeSecretClient{ResolveFunc: func(c context.Context, ref string) (string, error) {
			if ref == "b" {
				reads.Add(1)
				close(started)
				select {
				case <-release:
				case <-c.Done():
					return "", c.Err()
				}
			}
			return "v", nil
		}}, nil
	}}})
	resolve(t, r, []string{"a"}, false)
	var wg sync.WaitGroup
	out := make(chan answer, 10)
	for range 10 {
		wg.Go(func() { l, e := r.Resolve(testContext(t), "p", profile, []string{"b"}, true); out <- answer{l, e} })
	}
	<-started
	close(release)
	wg.Wait()
	for range 10 {
		if receive(t, out).err != nil {
			t.Fatal("ref failed")
		}
	}
	if reads.Load() != 1 {
		t.Fatal("duplicate reference resolution")
	}
}

func TestNoInputDoesNotJoinBootstrap(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	r := resolver(t, auth.ResolverOptions{Provider: testutil.FakeProvider{BootstrapFunc: func(c context.Context, _ config.Profile) (auth.SecretClient, error) {
		close(started)
		select {
		case <-release:
			return client("v"), nil
		case <-c.Done():
			return nil, c.Err()
		}
	}}})
	out := make(chan answer, 1)
	go func() { l, e := r.Resolve(testContext(t), "p", profile, nil, false); out <- answer{l, e} }()
	<-started
	_, e := r.Resolve(testContext(t), "p", profile, nil, true)
	if !errors.Is(e, auth.ErrRequired) {
		t.Fatal("no-input joined bootstrap")
	}
	close(release)
	if receive(t, out).err != nil {
		t.Fatal("interactive caller lost bootstrap")
	}
}

func TestAbandonedLateSuccessDiscarded(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	r := resolver(t, auth.ResolverOptions{AuthTimeout: 50 * time.Millisecond, Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) {
		close(started)
		<-release
		return client("late"), nil
	}}})
	_, e := r.Resolve(testContext(t), "p", profile, nil, false)
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("watchdog classification")
	}
	<-started
	close(release)
	waitNoSession(t, r)
}

func TestProviderRejectionInvalidatesSession(t *testing.T) {
	var reject atomic.Bool
	var boots atomic.Int64
	r := resolver(t, auth.ResolverOptions{Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) {
		boots.Add(1)
		return testutil.FakeSecretClient{ResolveFunc: func(context.Context, string) (string, error) {
			if reject.Load() {
				return "partial", errors.New("provider rejected")
			}
			return "v", nil
		}}, nil
	}}})
	resolve(t, r, []string{"a"}, false)
	reject.Store(true)
	l, e := r.Resolve(testContext(t), "p", profile, []string{"b"}, true)
	if !errors.Is(e, auth.ErrProvider) || len(l.Values) != 0 {
		t.Fatal("failure leaked lease")
	}
	_, e = r.Resolve(testContext(t), "p", profile, []string{"a"}, true)
	if !errors.Is(e, auth.ErrRequired) {
		t.Fatal("rejected session retained")
	}
	reject.Store(false)
	resolve(t, r, []string{"a"}, false)
	if boots.Load() != 2 {
		t.Fatal("later request did not reauthorize")
	}
}

func TestRefreshCannotReviveExpiredSession(t *testing.T) {
	clock := &clock{base: time.Now()}
	started, release := make(chan struct{}), make(chan struct{})
	r := resolver(t, auth.ResolverOptions{Now: clock.now, Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) {
		return testutil.FakeSecretClient{ResolveFunc: func(c context.Context, ref string) (string, error) {
			if ref == "b" {
				close(started)
				select {
				case <-release:
				case <-c.Done():
					return "", c.Err()
				}
			}
			return "v", nil
		}}, nil
	}}})
	resolve(t, r, []string{"a"}, false)
	clock.set(23 * time.Hour)
	out := make(chan answer, 1)
	go func() { l, e := r.Resolve(testContext(t), "p", profile, []string{"b"}, true); out <- answer{l, e} }()
	<-started
	clock.set(22 * time.Hour)
	_, e := r.Resolve(testContext(t), "p", profile, []string{"a"}, true)
	if !errors.Is(e, auth.ErrExpired) {
		t.Fatal("rollback accepted")
	}
	close(release)
	if !errors.Is(receive(t, out).err, auth.ErrExpired) {
		t.Fatal("refresh revived expired session")
	}
	_, e = r.Resolve(testContext(t), "p", profile, nil, true)
	if !errors.Is(e, auth.ErrExpired) {
		t.Fatal("expired client reinstalled")
	}
}
