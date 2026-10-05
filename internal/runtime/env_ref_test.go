package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/mcpclient"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
)

func TestEnvRefValues(t *testing.T) {
	c := config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Env: map[string]config.Value{"EXA_API_KEY": {Secret: &config.SecretRef{Secret: "env:EXA_API_KEY"}}}}}}
	in := map[string]string{"op://v/i/f": "lease"}
	before := maps.Clone(in)
	keychain := func(context.Context, string) (string, error) {
		t.Error("keychain consulted although the environment has the value")
		return "", errors.New("unexpected")
	}
	got, e := envRefValues(t.Context(), map[string]string{"EXA_API_KEY": "value-canary"}, keychain, c, in)
	if e != nil || got["env:EXA_API_KEY"] != "value-canary" || got["op://v/i/f"] != "lease" || !maps.Equal(in, before) {
		t.Fatal(got, e, in)
	}
	env, e := BuildChildEnv(nil, c, got)
	if e != nil || env["EXA_API_KEY"] != "value-canary" {
		t.Fatal(env, e)
	}
	missing := []func(context.Context, string) (string, error){
		nil,
		func(context.Context, string) (string, error) { return "", nil },
		func(context.Context, string) (string, error) {
			return "", errors.New("security: SecKeychainSearchCopyNext: stderr-canary value-canary")
		},
		func(context.Context, string) (string, error) { return "", context.DeadlineExceeded },
	}
	for i, lookup := range missing {
		for _, login := range []map[string]string{{}, {"EXA_API_KEY": ""}} {
			_, e = envRefValues(t.Context(), login, lookup, c, in)
			var out *output.Error
			if !errors.As(e, &out) || out.Code != "config_required" || !strings.Contains(out.Message, "EXA_API_KEY") || !strings.Contains(out.Message, "Keychain") || !strings.Contains(out.NextAction, `security add-generic-password -a "$USER" -s EXA_API_KEY -w`) || !strings.Contains(out.NextAction, "mcparcel runtime restart") || !strings.Contains(out.NextAction, "Environment: caller fallback") {
				t.Fatal(i, e)
			}
			b, _ := json.Marshal(out)
			if strings.Contains(string(b), "value-canary") || strings.Contains(string(b), "stderr-canary") || errors.Unwrap(out) != nil {
				t.Fatal(i, "error leaked lookup detail", string(b))
			}
		}
	}
}

func TestEnvRefValuesKeychainFallback(t *testing.T) {
	c := config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Env: map[string]config.Value{"EXA_API_KEY": {Secret: &config.SecretRef{Secret: "env:EXA_API_KEY"}}}}}}
	var asked []string
	keychain := func(_ context.Context, name string) (string, error) {
		asked = append(asked, name)
		return "keychain-canary", nil
	}
	for _, login := range []map[string]string{{}, {"EXA_API_KEY": ""}} {
		asked = nil
		got, e := envRefValues(t.Context(), login, keychain, c, nil)
		if e != nil || got["env:EXA_API_KEY"] != "keychain-canary" || len(asked) != 1 || asked[0] != "EXA_API_KEY" {
			t.Fatal(got, e, asked)
		}
	}
}

func TestKeychainRead(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	script := filepath.Join(p.Home, "security")
	write := func(body string) {
		if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write(`[ "$*" = "find-generic-password -a fixture-user -s EXA_API_KEY -w" ] || exit 9
printf 'line-canary\n'
`)
	if v, err := keychainRead(t.Context(), script, "fixture-user", "EXA_API_KEY"); err != nil || v != "line-canary" {
		t.Fatal(v, err)
	}
	write("echo 'security: The specified item could not be found in the keychain. stderr-canary' >&2\nexit 44\n")
	if v, err := keychainRead(t.Context(), script, "fixture-user", "EXA_API_KEY"); err == nil || v != "" || strings.Contains(err.Error(), "stderr-canary") {
		t.Fatal(v, err)
	}
	write("exec sleep 10\n")
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	if v, err := keychainRead(ctx, script, "fixture-user", "EXA_API_KEY"); err == nil || v != "" || time.Since(started) > 5*time.Second {
		t.Fatal(v, err, time.Since(started))
	}
}

func TestPoolEnvRefKeychainFallback(t *testing.T) {
	r := newRig(t)
	r.opts.Keychain = func(_ context.Context, name string) (string, error) {
		if name != "FIXTURE_KEYCHAIN_TOKEN" {
			return "", errors.New("not found")
		}
		return "kc-canary", nil
	}
	r.stdio("a", false)
	r.personal.Connections["a"].Transport.Stdio.Env["API_KEY"] = config.Value{Secret: &config.SecretRef{Secret: "env:FIXTURE_KEYCHAIN_TOKEN"}}
	r.personal.Connections["h"] = config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal("https://fixture.invalid/mcp"), Headers: map[string]config.Value{"Authorization": {Secret: &config.SecretRef{Secret: "env:FIXTURE_KEYCHAIN_TOKEN", Prefix: "Bearer "}}}}}}
	connect := r.opts.Connect
	r.opts.Connect = func(ctx context.Context, o mcpclient.ConnectOptions) (mcpclient.Session, error) {
		if o.Connection.Transport.HTTP != nil {
			r.captured <- o
			return nil, errors.New("fixture connect failure")
		}
		return connect(ctx, o)
	}
	r.start()
	count(t, r.call(testCtx(t), "a", "counter"))
	if o := <-r.captured; o.Env["API_KEY"] != "kc-canary" {
		t.Fatal("stdio env reference not resolved from keychain")
	}
	if res := r.call(testCtx(t), "h", "counter"); res.Error == nil {
		t.Fatal(res)
	}
	if o := <-r.captured; o.Headers["Authorization"] != "Bearer kc-canary" {
		t.Fatal("header environment reference not resolved from keychain")
	}
}

func TestPoolEnvRefNoCredentialResolve(t *testing.T) {
	r := newRig(t)
	r.opts.Credentials = auth.NewResolver(auth.ResolverOptions{Provider: testutil.FakeProvider{BootstrapFunc: func(context.Context, config.Profile) (auth.SecretClient, error) {
		t.Error("credential resolver used for environment reference")
		return nil, auth.ErrProvider
	}}})
	r.opts.LoginEnv["FIXTURE_TOKEN"] = "canary"
	r.stdio("a", false)
	r.personal.Connections["a"].Transport.Stdio.Env["API_KEY"] = config.Value{Secret: &config.SecretRef{Secret: "env:FIXTURE_TOKEN"}}
	r.personal.Connections["h"] = config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal("https://fixture.invalid/mcp"), Headers: map[string]config.Value{"Authorization": {Secret: &config.SecretRef{Secret: "env:FIXTURE_TOKEN", Prefix: "Bearer "}}}}}}
	connect := r.opts.Connect
	r.opts.Connect = func(ctx context.Context, o mcpclient.ConnectOptions) (mcpclient.Session, error) {
		if o.Connection.Transport.HTTP != nil {
			r.captured <- o
			return nil, errors.New("fixture connect failure")
		}
		return connect(ctx, o)
	}
	r.start()
	count(t, r.call(testCtx(t), "a", "counter"))
	if o := <-r.captured; o.Env["API_KEY"] != "canary" {
		t.Fatal("stdio env reference not resolved")
	}
	if res := r.call(testCtx(t), "h", "counter"); res.Error == nil {
		t.Fatal(res)
	}
	if o := <-r.captured; o.Headers["Authorization"] != "Bearer canary" {
		t.Fatal("header environment reference not resolved")
	}
}

func TestPoolEnvRefAuthRequiredNextAction(t *testing.T) {
	r := newRig(t)
	r.opts.LoginEnv["FIXTURE_TOKEN"] = "stale-canary"
	r.personal.Connections["h"] = config.Connection{Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal("https://fixture.invalid/mcp"), Headers: map[string]config.Value{"Authorization": {Secret: &config.SecretRef{Secret: "env:FIXTURE_TOKEN", Prefix: "Bearer "}}}}}}
	r.opts.Connect = func(context.Context, mcpclient.ConnectOptions) (mcpclient.Session, error) {
		return nil, output.NewError("auth_required", nil)
	}
	r.start()
	res := r.call(testCtx(t), "h", "counter")
	if res.Error == nil || res.Error.Code != "auth_required" || !strings.Contains(res.Error.NextAction, "FIXTURE_TOKEN") || !strings.Contains(res.Error.NextAction, "mcparcel runtime restart") || strings.Contains(res.Error.NextAction, "1Password") {
		t.Fatal(res.Error)
	}
	if b, _ := json.Marshal(res.Error); strings.Contains(string(b), "stale-canary") {
		t.Fatal("error leaked value")
	}
}
