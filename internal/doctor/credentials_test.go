package doctor

import (
	"strings"
	"testing"
)

func opDocs(env, bootstrap string) docs {
	profile := `"p":{"mode":"desktop","account":"a"}`
	if bootstrap != "" {
		profile = `"p":{"mode":"desktop-service-account","account":"a","bootstrapRef":"` + bootstrap + `"}`
	}
	return docs{
		personal:   `{"schemaVersion":1,"credentialProfiles":{"team":{}},"connections":{"op":{"credentialProfile":"team","transport":{"type":"stdio","command":"/bin/sh","env":{"KEY":{"secret":"` + env + `"}}}}}}`,
		local:      `{"schemaVersion":1,"credentialProfiles":{` + profile + `}}`,
		selections: `{"schemaVersion":1,"revision":1,"connections":{"local:op":{"enabled":true,"credentialProfile":"p"}}}`,
	}
}

func TestProfileBoundRowOnly(t *testing.T) {
	checks := Offline(inputFor(t, opDocs("op://v/i/f", "")))
	c := find(t, checks, "credentials.profile", "local:op")
	if c.Status != OK || c.Message != "Bound to profile p (desktop)." {
		t.Fatal(c)
	}
	unbound := opDocs("op://v/i/f", "")
	unbound.selections = `{"schemaVersion":1,"revision":1,"connections":{"local:op":{"enabled":true}}}`
	checks = Offline(inputFor(t, unbound))
	absent(t, checks, "credentials.profile")
	want(t, find(t, checks, "config.connection", "local:op"), Fail, "config_required")
}

func TestOnePasswordRefCharacterRule(t *testing.T) {
	for _, tc := range []struct {
		env, bootstrap, status, where string
	}{
		{"op://Private Vault/item/field", "", Fail, "env KEY"},
		{"op://vault/item:x/field", "", Fail, "env KEY"},
		{"op://abcdefghijklmnopqrstuvwxyz/item.name_1/field-2", "", OK, ""},
		{"op://vault/item/section/field", "", OK, ""},
		{"op://vault/item/field", "op://Private Vault/token/credential", Fail, "profile bootstrapRef"},
	} {
		c := find(t, Offline(inputFor(t, opDocs(tc.env, tc.bootstrap))), "credentials.reference", "local:op")
		want(t, c, tc.status, "")
		if tc.status == Fail && c.Message != "A 1Password reference in "+tc.where+" has characters 1Password rejects; refer to the vault or item by its ID." {
			t.Fatal(c.Message)
		}
		if strings.Contains(c.Message, "op://") || strings.Contains(c.Message, "Vault") {
			t.Fatal("reference in message:", c.Message)
		}
	}
	c := find(t, Offline(inputFor(t, opDocs("op://v/i/f", "op://v/t/c"))), "credentials.reference", "local:op")
	if c.Message != "2 1Password references are well-formed." {
		t.Fatal(c.Message)
	}
}

func TestHeadlessSignInFails(t *testing.T) {
	d := docs{
		personal:   `{"schemaVersion":1,"connections":{"pkce":{"transport":{"type":"http","url":"https://a.example.invalid/mcp"},"auth":{"type":"oauth"}},"cc":{"transport":{"type":"http","url":"https://b.example.invalid/mcp"},"auth":{"type":"oauth","grant":"client_credentials","tokenUrl":"https://b.example.invalid/token","clientId":{"secret":"env:CC_ID"},"clientSecret":{"secret":"env:CC_SECRET"}}}}}`,
		selections: `{"schemaVersion":1,"revision":1,"connections":{"local:pkce":{"enabled":true},"local:cc":{"enabled":true}}}`,
	}
	checks := Offline(headlessInput(inputFor(t, d)))
	c := find(t, checks, "credentials.oauth", "local:pkce")
	want(t, c, Fail, "auth_required")
	if c.Message != "This server needs sign-in, which headless mode cannot do." {
		t.Fatal(c.Message)
	}
	for _, c := range checks {
		if c.ID == "credentials.oauth" && c.Subject == "local:cc" {
			t.Fatal("client_credentials needs no sign-in")
		}
	}
}

func TestHeadlessEnvPresenceNamesOnly(t *testing.T) {
	d := docs{
		personal:   `{"schemaVersion":1,"connections":{"cc":{"transport":{"type":"http","url":"https://b.example.invalid/mcp","headers":{"X-Key":{"secret":"env:HDR_KEY"}}},"auth":{"type":"oauth","grant":"client_credentials","tokenUrl":"https://b.example.invalid/token","clientId":{"secret":"env:CC_ID"},"clientSecret":{"secret":"env:CC_SECRET"}}}}}`,
		selections: `{"schemaVersion":1,"revision":1,"connections":{"local:cc":{"enabled":true}}}`,
	}
	in := headlessInput(inputFor(t, d))
	set := map[string]bool{"CC_ID": true, "CC_SECRET": true}
	asked := []string{}
	in.LookupEnv = func(name string) bool { asked = append(asked, name); return set[name] }
	c := find(t, Offline(in), "credentials.env", "local:cc")
	want(t, c, Warn, "")
	if c.Message != "Environment variable HDR_KEY is not set in this shell; the runtime reads its own environment." || c.NextAction != "Check the env: map of the wrapper that runs mcparcel (headless.md)." {
		t.Fatal(c)
	}
	set["HDR_KEY"] = true
	if c = find(t, Offline(in), "credentials.env", "local:cc"); c.Status != OK || c.Message != "All 3 variables are set here." {
		t.Fatal(c)
	}
	if strings.Join(asked, ",") == "" {
		t.Fatal("LookupEnv unused")
	}
}

func TestDesktopEmitsNoEnvOrOAuthRows(t *testing.T) {
	d := docs{
		personal:   `{"schemaVersion":1,"connections":{"pkce":{"transport":{"type":"stdio","command":"/bin/sh","env":{"K":{"secret":"env:API_KEY"}}}},"web":{"transport":{"type":"http","url":"https://a.example.invalid/mcp"},"auth":{"type":"oauth"}}}}`,
		selections: `{"schemaVersion":1,"revision":1,"connections":{"local:pkce":{"enabled":true},"local:web":{"enabled":true}}}`,
	}
	in := inputFor(t, d)
	in.LookupEnv = func(string) bool { t.Fatal("desktop doctor looked at the environment"); return false }
	checks := Offline(in)
	absent(t, checks, "credentials.env")
	absent(t, checks, "credentials.oauth")
	absent(t, checks, "credentials.reference")
	absent(t, checks, "prereq.onepassword")
}
