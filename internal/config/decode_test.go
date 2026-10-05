package config

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func personalTransport(transport string) []byte {
	return []byte(`{"schemaVersion":1,"connections":{"fixture":{"transport":` + transport + `}}}`)
}

func TestDecodeStdioAndHTTP(t *testing.T) {
	p, err := DecodePersonal(personalTransport(`{"type":"stdio","command":"fixture","env":{"X":"${TOKEN}"}}`))
	if err != nil {
		t.Fatal(err)
	}
	c := p.Connections["fixture"]
	if c.CallTimeout != "120s" || c.Transport.Stdio == nil || c.Transport.HTTP != nil || c.Transport.Stdio.Env["X"].Literal == nil || *c.Transport.Stdio.Env["X"].Literal != "${TOKEN}" {
		t.Fatal(c)
	}
	p, err = DecodePersonal(personalTransport(`{"type":"http","url":"http://127.0.0.1/mcp","allowInsecureHttp":"loopback"}`))
	if err != nil || p.Connections["fixture"].Transport.HTTP.Mode != "auto" {
		t.Fatal(p, err)
	}
	p, err = DecodePersonal(personalTransport(`{"type":"http","url":"https://fixture.invalid/mcp"}`))
	if err != nil || p.Connections["fixture"].Transport.HTTP.AllowInsecureHTTP != "never" {
		t.Fatal(p, err)
	}
	encoded, err := json.Marshal(c.Transport)
	if err != nil {
		t.Fatal(err)
	}
	var round Transport
	if err = json.Unmarshal(encoded, &round); err != nil || !reflect.DeepEqual(round, c.Transport) {
		t.Fatal(string(encoded), round, err)
	}
}

func TestDecodeSecretValue(t *testing.T) {
	data := []byte(`{"schemaVersion":1,"credentialProfiles":{"team":{}},"connections":{"fixture":{"credentialProfile":"team","transport":{"type":"http","url":"https://fixture.invalid","headers":{"Authorization":{"secret":"op://My Vault/My Item/section/API key","prefix":"Bearer ","suffix":"!"}}}}}}`)
	p, err := DecodePersonal(data)
	if err != nil {
		t.Fatal(err)
	}
	v := p.Connections["fixture"].Transport.HTTP.Headers["Authorization"]
	if v.Literal != nil || v.Secret == nil || *v.Secret != (SecretRef{Secret: "op://My Vault/My Item/section/API key", Prefix: "Bearer ", Suffix: "!"}) {
		t.Fatal(v)
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var round Value
	if err = json.Unmarshal(encoded, &round); err != nil || !reflect.DeepEqual(v, round) {
		t.Fatal(round, err)
	}
}

func TestDecodeRejectsUnknownAndDuplicate(t *testing.T) {
	for _, tc := range []struct{ data, path string }{
		{string(personalTransport(`{"type":"stdio","command":"fixture","env":{"X":"sensitive-value","X":"other"}}`)), "connections.fixture.transport.env.X"},
		{string(personalTransport(`{"type":"stdio","command":"fixture","unexpected":"sensitive-value"}`)), "connections.fixture.transport.unexpected"},
		{`{"schemaVersion":2,"connections":{}}`, "schemaVersion"},
	} {
		_, err := DecodePersonal([]byte(tc.data))
		if !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), tc.path) || strings.Contains(err.Error(), "sensitive-value") {
			t.Fatal(err)
		}
	}
}

func TestDecodeFullMetadata(t *testing.T) {
	for _, field := range []string{`"domains":[]`, `"toolPolicy":{}`, `"lifecycle":{}`, `"inputs":{}`} {
		if _, err := DecodePersonal([]byte(`{"schemaVersion":1,"connections":{"fixture":{` + field + `,"transport":{"type":"stdio","command":"fixture"}}}}`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := DecodePersonal(personalTransport(`{"type":"http","url":"https://fixture.invalid","mode":"sse"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePersonal([]byte(`{"schemaVersion":1,"connections":{"fixture":{"selection":{},"transport":{"type":"stdio","command":"fixture"}}}}`)); !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), "unknown field") {
		t.Fatal(err)
	}
}

func TestDecodeRejectsMixedUnion(t *testing.T) {
	for _, s := range []string{
		`{"type":"stdio","command":"fixture","env":{"X":{"secret":"op://v/i/f","input":"x"}}}`,
		`{"type":"stdio","command":["fixture",{}]}`,
		`{"type":"http","url":"https://fixture.invalid","command":"fixture"}`,
		`{"type":"stdio","command":"fixture","url":"https://fixture.invalid"}`,
	} {
		if _, err := DecodePersonal(personalTransport(s)); !errors.Is(err, ErrConfig) {
			t.Fatal(err)
		}
	}
	for _, v := range []string{`null`, `1`, `{"secret":"op://v/i/f","literal":"x"}`} {
		var value Value
		if err := json.Unmarshal([]byte(v), &value); !errors.Is(err, ErrConfig) {
			t.Fatal(err)
		}
	}
	if _, err := json.Marshal(Transport{Stdio: &Stdio{}, HTTP: &HTTP{}}); err == nil {
		t.Fatal("marshaled mixed transport")
	}
}

func TestHTTPConsent(t *testing.T) {
	for _, tc := range []struct {
		url, consent string
		ok           bool
	}{
		{"http://fixture.invalid", "", false},
		{"http://127.0.0.1", "loopback", true},
		{"http://[::1]", "loopback", true},
		{"http://localhost", "loopback", true},
		{"http://fixture.invalid", "loopback", false},
		{"http://fixture.invalid", "explicit", true},
		{"https://user:secret@fixture.invalid", "", false},
		{"https://fixture.invalid/#fragment", "", false},
	} {
		b, _ := json.Marshal(map[string]string{"type": "http", "url": tc.url, "allowInsecureHttp": tc.consent})
		_, err := DecodePersonal(personalTransport(string(b)))
		if (err == nil) != tc.ok {
			t.Fatal(tc, err)
		}
	}
}

func TestProtectedEnv(t *testing.T) {
	names := []string{"OP_SERVICE_ACCOUNT_TOKEN", "OP_CONNECT_TOKEN", "GH_TOKEN", "GITHUB_TOKEN", "GITHUB_API_TOKEN", "GIT_ASKPASS", "SSH_AUTH_SOCK", "OP_X", "AWS_X", "AZURE_X", "GOOGLE_X", "DYLD_X", "LD_X", "BASH_ENV", "ENV", "ZDOTDIR"}
	for _, name := range names {
		if !ProtectedEnv(name) {
			t.Fatal(name)
		}
		b, _ := json.Marshal(map[string]any{"type": "stdio", "command": "fixture", "inheritEnv": []string{name}})
		if _, err := DecodePersonal(personalTransport(string(b))); !errors.Is(err, ErrConfig) {
			t.Fatal(name, err)
		}
	}
	if ProtectedEnv("FIXTURE_FEATURE") || ProtectedEnv("op_X") {
		t.Fatal("case-sensitive predicate")
	}
	if _, err := DecodePersonal(personalTransport(`{"type":"stdio","command":"fixture","inheritEnv":["FIXTURE_FEATURE"]}`)); err != nil {
		t.Fatal(err)
	}
	_, err := DecodePersonal([]byte(`{"schemaVersion":1,"credentialProfiles":{"team":{}},"connections":{"fixture":{"credentialProfile":"team","transport":{"type":"stdio","command":"fixture","env":{"GITHUB_TOKEN":{"secret":"op://v/i/f"}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"OP_SERVICE_ACCOUNT_TOKEN", "OP_CONNECT_TOKEN", "BASH_ENV", "ENV", "ZDOTDIR", "DYLD_X", "LD_X"} {
		b, _ := json.Marshal(map[string]any{"type": "stdio", "command": "fixture", "env": map[string]string{name: "literal"}})
		if _, err := DecodePersonal(personalTransport(string(b))); !errors.Is(err, ErrConfig) {
			t.Fatal(name, err)
		}
	}
}

func TestForeignOwnerStat(t *testing.T) {
	st := unix.Stat_t{Uid: uint32(os.Getuid() + 1), Mode: unix.S_IFREG | 0o600, Nlink: 1}
	if err := privateFileStat(&st, os.Getuid()); !errors.Is(err, ErrUnsafePath) {
		t.Fatal(err)
	}
	st.Uid = uint32(os.Getuid())
	if err := privateFileStat(&st, os.Getuid()); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeValidation(t *testing.T) {
	for _, s := range []string{`null`, `{"schemaVersion":1,"connections":null}`, `{"schemaVersion":1,"connections":{"Bad":{}}}`, `{"schemaVersion":1,"connections":{"fixture":{}}}`} {
		if _, err := DecodePersonal([]byte(s)); !errors.Is(err, ErrConfig) {
			t.Fatal(s, err)
		}
	}
	for _, tr := range []string{
		`{"type":"stdio","command":null}`, `{"type":"stdio","command":""}`, `{"type":"stdio","command":"op://v/i/f"}`, `{"type":"stdio","command":"fixture","args":[null]}`, `{"type":"stdio","command":"fixture","args":["op://v/i/f"]}`, `{"type":"stdio","command":"fixture","cwd":"relative"}`, `{"type":"stdio","command":"fixture","env":{"bad-name":"x"}}`, `{"type":"stdio","command":"fixture","env":{"X":"a\u0000b"}}`, `{"type":"http","url":"https://fixture.invalid","headers":{"X-Test":"a\r\nb"}}`, `{"type":"http","url":"https://fixture.invalid","headers":{"bad header":"x"}}`,
	} {
		if _, err := DecodePersonal(personalTransport(tr)); !errors.Is(err, ErrConfig) {
			t.Fatal(tr, err)
		}
	}
	for _, ref := range []string{"op://v/i/f", "op://My Vault/My Item/My Section/My Field"} {
		b, _ := json.Marshal(map[string]any{"schemaVersion": 1, "credentialProfiles": map[string]any{"team": map[string]string{"mode": "desktop-service-account", "account": "Fixture", "bootstrapRef": ref}}})
		l, err := DecodeLocal(b)
		if err != nil || l.CredentialProfiles["team"].SessionDuration != "24h" {
			t.Fatal(l, err)
		}
	}
	for _, ref := range []string{"op://v/i", "op://v//f", "op://v/i/f/", "op://u@v/i/f", "op://v/i/f?x", "op://v/i/f#x", "op://v/i/f\x00", "op://v/i/s/f/x"} {
		b, _ := json.Marshal(map[string]any{"schemaVersion": 1, "credentialProfiles": map[string]any{"team": map[string]string{"mode": "desktop-service-account", "account": "Fixture", "bootstrapRef": ref}}})
		if _, err := DecodeLocal(b); !errors.Is(err, ErrConfig) || strings.Contains(err.Error(), ref) {
			t.Fatal(ref, err)
		}
	}
	for _, duration := range []string{"0s", "-1s", "25h", "not-a-duration"} {
		b, _ := json.Marshal(map[string]any{"schemaVersion": 1, "credentialProfiles": map[string]any{"team": map[string]string{"mode": "desktop-service-account", "account": "Fixture", "bootstrapRef": "op://v/i/f", "sessionDuration": duration}}})
		if _, err := DecodeLocal(b); !errors.Is(err, ErrConfig) {
			t.Fatal(err)
		}
	}
}

func TestDecodeMalformedArray(t *testing.T) {
	for _, input := range []string{`{"schemaVersion":1,"connections":[NaN]}`, `{"schemaVersion":1,"connections":[}`, `{"schemaVersion":1,"connections":{"fixture":` + strings.Repeat("[", 129) + `0` + strings.Repeat("]", 129) + `}}`} {
		if _, err := DecodePersonal([]byte(input)); !errors.Is(err, ErrConfig) {
			t.Fatal(err)
		}
	}
}
