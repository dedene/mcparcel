package auth_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/testutil"
)

func sampleState() auth.OAuthState {
	return auth.OAuthState{
		Version: 1, URL: "https://mcp.fixture.invalid/mcp", Issuer: "https://auth.fixture.invalid", Resource: "https://mcp.fixture.invalid/mcp",
		TokenURL: "https://auth.fixture.invalid/token", AuthStyle: 1, ClientID: "client-fixture", ClientSecret: "client-secret-fixture",
		RefreshToken: "refresh-fixture", AccessExpiry: 1_900_000_000, Failure: &auth.OAuthFailure{At: 1_800_000_000, Code: "invalid_grant"},
	}
}

func TestOAuthStateRoundTrip(t *testing.T) {
	k := &testutil.MemKeyring{}
	ctx := context.Background()
	in := sampleState()
	if err := auth.SaveOAuth(ctx, k, "local:linear", in); err != nil {
		t.Fatal(err)
	}
	raw, _ := k.Get(auth.KeyringService, "local:linear")
	for _, key := range []string{`"v":1`, `"u":`, `"i":`, `"r":`, `"t":`, `"a":1`, `"c":`, `"s":`, `"rt":`, `"e":`, `"f":{"at":`} {
		if !strings.Contains(raw, key) {
			t.Fatalf("stored JSON lacks %s", key)
		}
	}
	out, err := auth.LoadOAuth(ctx, k, "local:linear")
	if err != nil {
		t.Fatal(err)
	}
	if out.URL != in.URL || out.Issuer != in.Issuer || out.Resource != in.Resource || out.TokenURL != in.TokenURL || out.AuthStyle != in.AuthStyle ||
		out.ClientID != in.ClientID || out.ClientSecret != in.ClientSecret || out.RefreshToken != in.RefreshToken || out.AccessExpiry != in.AccessExpiry ||
		out.Failure == nil || *out.Failure != *in.Failure || out.Version != 1 {
		t.Fatal("round trip changed the state")
	}
	minimal := auth.OAuthState{Version: 1, URL: "u", Issuer: "i", Resource: "r", TokenURL: "t"}
	if err := auth.SaveOAuth(ctx, k, "local:min", minimal); err != nil {
		t.Fatal(err)
	}
	if got, _ := k.Get(auth.KeyringService, "local:min"); got != `{"v":1,"u":"u","i":"i","r":"r","t":"t"}` {
		t.Fatalf("not compact: %s", got)
	}
}

func TestSaveOAuthBudget(t *testing.T) {
	k := &testutil.MemKeyring{}
	account := "local:" + strings.Repeat("a", 194)
	s := sampleState()
	s.RefreshToken = strings.Repeat("r", 2000)
	if err := auth.SaveOAuth(context.Background(), k, account, s); err != nil {
		t.Fatal(err)
	}
	sets := 0
	k.BeforeSet = func() { sets++ }
	s.RefreshToken = strings.Repeat("r", 3000)
	e := code(t, auth.SaveOAuth(context.Background(), k, account, s), "keychain_unavailable")
	if e.Message != "The sign-in is too large for one Keychain item." || sets != 0 {
		t.Fatalf("message %q, sets %d", e.Message, sets)
	}
}

func TestKeyringFailureNoFallback(t *testing.T) {
	dirs := map[string]string{}
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		dirs[name] = t.TempDir()
		t.Setenv(name, dirs[name])
	}
	k := &testutil.MemKeyring{Err: errors.New("security: secret-canary failure")}
	ctx := context.Background()
	err := auth.SaveOAuth(ctx, k, "local:linear", sampleState())
	code(t, err, "keychain_unavailable")
	_, err2 := auth.LoadOAuth(ctx, k, "local:linear")
	code(t, err2, "keychain_unavailable")
	_, err3 := auth.DeleteOAuth(ctx, k, "local:linear")
	code(t, err3, "keychain_unavailable")
	for _, e := range []error{err, err2, err3} {
		if strings.Contains(e.Error(), "canary") || errors.Is(e, k.Err) {
			t.Fatal("keyring error leaked")
		}
	}
	for name, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("%s not empty: %v %v", name, entries, err)
		}
	}
}

func TestKeyringNilIsUnavailable(t *testing.T) {
	ctx := context.Background()
	code(t, auth.SaveOAuth(ctx, nil, "local:linear", sampleState()), "keychain_unavailable")
	_, err := auth.LoadOAuth(ctx, nil, "local:linear")
	code(t, err, "keychain_unavailable")
	_, err = auth.DeleteOAuth(ctx, nil, "local:linear")
	code(t, err, "keychain_unavailable")
}

func TestDeleteOAuthMissing(t *testing.T) {
	k := &testutil.MemKeyring{}
	ctx := context.Background()
	removed, err := auth.DeleteOAuth(ctx, k, "local:linear")
	if removed || err != nil {
		t.Fatal(removed, err)
	}
	if err := auth.SaveOAuth(ctx, k, "local:linear", sampleState()); err != nil {
		t.Fatal(err)
	}
	removed, err = auth.DeleteOAuth(ctx, k, "local:linear")
	if !removed || err != nil {
		t.Fatal(removed, err)
	}
	if _, err := auth.LoadOAuth(ctx, k, "local:linear"); !errors.Is(err, auth.ErrNoSession) {
		t.Fatal(err)
	}
}

func TestLoadOAuthCorruptIsNoSession(t *testing.T) {
	ctx := context.Background()
	for _, raw := range []string{"", "not json", `{"v":2,"u":"u","i":"i","r":"r","t":"t"}`, `{"v":1,"u":"u","x":1}`, `[1]`} {
		k := &testutil.MemKeyring{}
		if err := k.Set(auth.KeyringService, "local:linear", raw); err != nil {
			t.Fatal(err)
		}
		if _, err := auth.LoadOAuth(ctx, k, "local:linear"); !errors.Is(err, auth.ErrNoSession) {
			t.Fatalf("%q: %v", raw, err)
		}
	}
}

func TestOAuthStateRedacted(t *testing.T) {
	s := sampleState()
	s.RefreshToken, s.ClientSecret = "rt-canary", "secret-canary"
	for _, f := range []string{"%v", "%+v", "%#v", "%s"} {
		for _, v := range []any{s, &s} {
			if got := fmt.Sprintf(f, v); strings.Contains(got, "canary") || !strings.Contains(got, "OAuthState{redacted}") {
				t.Fatalf("%s: %s", f, got)
			}
		}
	}
}
