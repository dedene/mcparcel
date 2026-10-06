package runtime

import (
	"testing"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

func withHealth(r *poolRig) *auth.Health {
	h := auth.NewHealth(r.paths.StateDir, nil)
	r.opts.Health = h
	return h
}

func healthEvents(t *testing.T, r *poolRig) []auth.HealthEvent {
	t.Helper()
	m, e := auth.ReadHealth(r.paths.StateDir)
	if e != nil {
		t.Fatal(e)
	}
	return m["local:a"].Events
}

func TestPoolLogoutRecordsHealth(t *testing.T) {
	r, _, _ := oauthRig(t, testutil.AuthServerOptions{ClientID: "pre-id"}, true)
	withHealth(r)
	r.start()
	b := &loginBrowser{visit: true}
	success(t, r.login(testCtx(t), b, false))
	b.wg.Wait()
	success(t, r.logout(testCtx(t)))
	events := healthEvents(t, r)
	if len(events) != 2 || events[0].Kind != auth.HealthAuthorized || events[1].Kind != auth.HealthLogout {
		t.Fatalf("%+v", events)
	}
	// Nothing removed, nothing recorded.
	success(t, r.logout(testCtx(t)))
	if n := len(healthEvents(t, r)); n != 2 {
		t.Fatal("events", n)
	}
}

func TestPoolLoginPassesPreviousRegistration(t *testing.T) {
	r, as, kr := oauthRig(t, testutil.AuthServerOptions{Registration: true}, false)
	c := r.personal.Connections["a"]
	c.Auth = &config.OAuth{Type: "oauth", TokenEndpointAuthMethod: "client_secret_post"}
	r.personal.Connections["a"] = c
	withHealth(r)
	r.start()
	for range 2 {
		b := &loginBrowser{visit: true}
		success(t, r.login(testCtx(t), b, false))
		b.wg.Wait()
	}
	if n, _, _ := as.Counts(); n != 1 {
		t.Fatal("registrations", n)
	}
	events := healthEvents(t, r)
	if len(events) != 2 || events[1].Kind != auth.HealthAuthorized || !events[1].ReusedClient {
		t.Fatalf("%+v", events)
	}
	if s, e := auth.LoadOAuth(testCtx(t), kr, "local:a"); e != nil || s.ClientID == "" {
		t.Fatal(e)
	}
	count(t, r.call(testCtx(t), "a", "counter"))
}

func TestPoolRememberSignInRecordsOnce(t *testing.T) {
	r, _, _ := oauthRig(t, testutil.AuthServerOptions{Registration: true}, false)
	withHealth(r)
	r.start()
	signInAction(t, r.call(testCtx(t), "a", "counter"))
	signInAction(t, r.call(testCtx(t), "a", "counter"))
	events := healthEvents(t, r)
	if len(events) != 1 || events[0].Kind != auth.HealthReauthorizationRequired || events[0].Code != "server_requested" || !events[0].Terminal {
		t.Fatalf("%+v", events)
	}
}
