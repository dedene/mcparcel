package config_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

const fullCatalog = `{"schemaVersion":1,"name":"Team","domains":{"dev":{"label":"Development"}},"credentialProfiles":{"team":{"description":"Team credentials"}},"connections":{"paper":{"label":"Paper","description":"Documents","domains":["dev"],"inputs":{"host":{"kind":"url","description":"Endpoint","default":"https://fixture.invalid"}},"credentialProfile":"team","transport":{"type":"http","url":{"input":"host"},"headers":{"X-Token":{"secret":"op://v/i/f"}},"mode":"sse"},"auth":{"type":"oauth","clientName":"Paper","scopes":["read","write"],"clientId":"public-client","clientSecret":{"secret":"op://v/i/secret"},"tokenEndpointAuthMethod":"client_secret_post","redirectUrl":"http://127.0.0.1/callback","issuerUrl":"https://issuer.invalid"},"toolPolicy":{"allow":["read"],"deny":["write"]},"lifecycle":{"idleTimeout":"5m"},"callTimeout":"30s","startupTimeout":"120s"}}}`

func TestStrictCatalog(t *testing.T) {
	if _, err := config.DecodeCatalog([]byte(fullCatalog)); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, raw, path string }{
		{"duplicate", strings.Replace(fullCatalog, `"clientName":"Paper"`, `"clientName":"Paper","clientName":"hidden-value"`, 1), "connections.paper.auth.clientName"},
		{"unknownOAuth", strings.Replace(fullCatalog, `"clientName":"Paper"`, `"unknown":"hidden-value"`, 1), "connections.paper.auth.unknown"},
		{"version", strings.Replace(fullCatalog, `"schemaVersion":1`, `"schemaVersion":2`, 1), "schemaVersion"},
		{"mixedUnion", `{"schemaVersion":1,"connections":{"paper":{"transport":{"type":"stdio","command":{"input":"hidden-value","secret":"op://v/i/f"}}}}}`, "connections.paper.transport.command"},
		{"missingCommand", `{"schemaVersion":1,"connections":{"paper":{"transport":{"type":"stdio"}}}}`, "connections.paper.transport.command"},
		{"undeclaredInput", strings.Replace(fullCatalog, `"input":"host"`, `"input":"hidden-value"`, 1), "connections.paper.transport.url"},
		{"undeclaredProfile", strings.Replace(fullCatalog, `"credentialProfile":"team"`, `"credentialProfile":"hidden-value"`, 1), "connections.paper.credentialProfile"},
		{"undeclaredDomain", strings.Replace(fullCatalog, `"domains":["dev"]`, `"domains":["hidden-value"]`, 1), "connections.paper.domains"},
		{"zeroStartup", strings.Replace(fullCatalog, `"startupTimeout":"120s"`, `"startupTimeout":"0s"`, 1), "connections.paper.startupTimeout"},
		{"badStartup", strings.Replace(fullCatalog, `"startupTimeout":"120s"`, `"startupTimeout":"hidden-value"`, 1), "connections.paper.startupTimeout"},
		{"emptyStartup", strings.Replace(fullCatalog, `"startupTimeout":"120s"`, `"startupTimeout":""`, 1), "connections.paper.startupTimeout"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := config.DecodeCatalog([]byte(tt.raw))
			if !errors.Is(err, config.ErrConfig) || !strings.Contains(err.Error(), tt.path) || strings.Contains(err.Error(), "hidden-value") {
				t.Fatalf("error = %v; want ErrConfig at %s without value", err, tt.path)
			}
		})
	}
}

func TestCatalogDefaults(t *testing.T) {
	c, err := config.DecodeCatalog([]byte(`{"schemaVersion":1,"connections":{"stdio":{"transport":{"type":"stdio","command":"fixture"}},"http":{"transport":{"type":"http","url":"https://fixture.invalid"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Domains == nil || len(c.Domains) != 0 || c.CredentialProfiles == nil || len(c.CredentialProfiles) != 0 {
		t.Fatal(c)
	}
	for _, conn := range c.Connections {
		if conn.CallTimeout != "120s" {
			t.Fatal(conn.CallTimeout)
		}
	}
	h := c.Connections["http"].Transport.HTTP
	if h.Mode != "auto" || h.AllowInsecureHTTP != "never" {
		t.Fatal(h)
	}
}

func TestPolicyAllowPresence(t *testing.T) {
	for _, tt := range []struct {
		name, policy     string
		invalid, omitted bool
	}{
		{"omitted", `{}`, false, true}, {"empty", `{"allow":[]}`, false, false}, {"null", `{"allow":null}`, true, false}, {"duplicate", `{"allow":["read","read"]}`, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, err := config.DecodeCatalog([]byte(`{"schemaVersion":1,"connections":{"paper":{"transport":{"type":"stdio","command":"fixture"},"toolPolicy":` + tt.policy + `}}}`))
			if tt.invalid {
				if !errors.Is(err, config.ErrConfig) {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			allow := c.Connections["paper"].ToolPolicy.Allow
			if tt.omitted {
				if allow != nil {
					t.Fatal(allow)
				}
			} else if allow == nil || *allow == nil || len(*allow) != 0 {
				t.Fatal(allow)
			}
		})
	}
}

func TestCatalogOAuth(t *testing.T) {
	c, err := config.DecodeCatalog([]byte(fullCatalog))
	if err != nil {
		t.Fatal(err)
	}
	auth := c.Connections["paper"].Auth
	if auth.Type != "oauth" || auth.ClientName != "Paper" || !reflect.DeepEqual(auth.Scopes, []string{"read", "write"}) || auth.ClientID == nil || auth.ClientID.Literal == nil || *auth.ClientID.Literal != "public-client" || auth.ClientSecret == nil || auth.ClientSecret.Secret == nil || auth.ClientSecret.Secret.Secret != "op://v/i/secret" || auth.TokenEndpointAuthMethod != "client_secret_post" || auth.RedirectURL != "http://127.0.0.1/callback" || auth.IssuerURL != "https://issuer.invalid" {
		t.Fatal(auth)
	}
	if !reflect.DeepEqual(config.SecretRefs(c.Connections["paper"]), []string{"op://v/i/f", "op://v/i/secret"}) {
		t.Fatal(config.SecretRefs(c.Connections["paper"]))
	}
	for _, raw := range []string{strings.Replace(fullCatalog, `"X-Token"`, `"aUtHoRiZaTiOn"`, 1), strings.Replace(fullCatalog, `"clientSecret":{"secret":"op://v/i/secret"}`, `"clientSecret":"hidden-value"`, 1), strings.Replace(fullCatalog, `"clientSecret":{"secret":"op://v/i/secret"}`, `"clientSecret":{"input":"host"}`, 1)} {
		_, err := config.DecodeCatalog([]byte(raw))
		if !errors.Is(err, config.ErrConfig) || strings.Contains(err.Error(), "hidden-value") {
			t.Fatal(err)
		}
	}
}

func TestLoadFullCatalogFields(t *testing.T) {
	for _, field := range []string{`"toolPolicy":{}`, `"auth":{"type":"oauth"}`, `"inputs":{"unused":{"kind":"string","description":"Input"}}`} {
		t.Run(field, func(t *testing.T) {
			p, _ := testutil.IsolatedPaths(t)
			writeFile(t, p.PersonalFile, `{"schemaVersion":1,"connections":{"paper":{"transport":{"type":"http","url":"https://fixture.invalid"},`+field+`}}}`, 0o644)
			snapshot, err := config.Load(p)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = snapshot.RuntimeConnection("paper")
			expected := error(nil)
			if strings.Contains(field, `"inputs"`) {
				expected = config.ErrConfigRequired
			}
			if !errors.Is(err, expected) {
				t.Fatal(err)
			}
		})
	}
}

func TestValidateStateRejectsCatalogEnvRef(t *testing.T) {
	c, err := config.DecodeCatalog([]byte(`{"schemaVersion":1,"connections":{"exa":{"transport":{"type":"stdio","command":"npx","env":{"EXA_API_KEY":{"secret":"env:EXA_API_KEY"}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	local, err := config.DecodeLocal([]byte(`{"schemaVersion":1,"sources":[{"id":"github-42","repositoryId":42,"owner":"owner","repo":"repo","path":"catalog.json","ref":"main","commit":"0123456789abcdef0123456789abcdef01234567","pinned":false}]}`))
	if err != nil {
		t.Fatal(err)
	}
	empty := config.Catalog{SchemaVersion: 1, Connections: map[string]config.Connection{}}
	selections := config.Selections{SchemaVersion: 1, Connections: map[string]config.Selection{}}
	err = config.ValidateState(config.State{Local: local, Personal: empty, Selections: selections, Catalogs: map[string]config.Catalog{"github-42": c}})
	if !errors.Is(err, config.ErrConfig) || !strings.Contains(err.Error(), "catalogs.github-42.connections.exa") {
		t.Fatal(err)
	}
	err = config.ValidateState(config.State{Local: config.Local{SchemaVersion: 1}, Personal: c, Selections: selections})
	if err != nil {
		t.Fatal(err)
	}
}

// ccDoc is a personal definition with a client_credentials connection; auth
// and transport replace the defaults when nonempty.
func ccDoc(auth, transport string) string {
	if auth == "" {
		auth = `"grant":"client_credentials","tokenUrl":"https://ws.example.invalid/oauth/token","clientId":{"secret":"env:CC_ID"},"clientSecret":{"secret":"env:CC_SECRET"}`
	}
	if transport == "" {
		transport = `{"type":"http","url":"https://mcp.example.invalid/mcp"}`
	}
	return `{"schemaVersion":1,"connections":{"front":{"transport":` + transport + `,"auth":{"type":"oauth",` + auth + `}}}}`
}

// Each client_credentials rule has an accept and a reject case, checked by
// both the Go decoder and schema/catalog.schema.json.
func TestClientCredentialsValidation(t *testing.T) {
	const (
		id     = `"clientId":{"secret":"env:CC_ID"}`
		secret = `"clientSecret":{"secret":"env:CC_SECRET"}`
		cc     = `"grant":"client_credentials",`
		tok    = `"tokenUrl":"https://ws.example.invalid/oauth/token",`
		creds  = id + "," + secret
	)
	loopback := `{"type":"http","url":"http://127.0.0.1/mcp","allowInsecureHttp":"loopback"}`
	for _, tt := range []struct {
		name, auth, transport string
		schema, goValid       bool
		path                  string
	}{
		{"accept/minimal", "", "", true, true, ""},
		{"accept/defaultGrantWithoutTokenURL", creds, "", true, true, ""},
		{"accept/explicitAuthorizationCode", `"grant":"authorization_code",` + creds, "", true, true, ""},
		{"reject/unknownGrant", `"grant":"password",` + tok + creds, "", false, false, "connections.front.auth.grant"},
		{"reject/stdioTransport", "", `{"type":"stdio","command":"fixture"}`, true, false, "connections.front.auth"},
		{"reject/missingTokenURL", cc + creds, "", false, false, "connections.front.auth.tokenUrl"},
		{"reject/tokenURLWithoutGrant", tok + creds, "", false, false, "connections.front.auth.tokenUrl"},
		{"reject/invalidTokenURL", cc + `"tokenUrl":"not a url",` + creds, "", true, false, "connections.front.auth.tokenUrl"},
		{"reject/httpTokenURL", cc + `"tokenUrl":"http://ws.example.invalid/oauth/token",` + creds, loopback, true, false, "connections.front.auth.tokenUrl"},
		{"accept/loopbackTokenURL", cc + `"tokenUrl":"http://127.0.0.1:9/oauth/token",` + creds, loopback, true, true, ""},
		{"reject/loopbackTokenURLWithoutConsent", cc + `"tokenUrl":"http://127.0.0.1:9/oauth/token",` + creds, "", true, false, "connections.front.auth.tokenUrl"},
		{"accept/literalClientID", cc + tok + `"clientId":"front-client",` + secret, "", true, true, ""},
		{"reject/missingClientID", cc + tok + secret, "", false, false, "connections.front.auth.clientId"},
		{"reject/missingClientSecret", cc + tok + id, "", false, false, "connections.front.auth.clientSecret"},
		{"reject/literalClientSecret", cc + tok + id + `,"clientSecret":"hidden-value"`, "", false, false, "connections.front.auth.clientSecret"},
		{"reject/redirectURL", cc + tok + creds + `,"redirectUrl":"http://127.0.0.1/callback"`, "", false, false, "connections.front.auth.redirectUrl"},
		{"reject/issuerURL", cc + tok + creds + `,"issuerUrl":"https://issuer.example.invalid"`, "", false, false, "connections.front.auth.issuerUrl"},
		{"reject/clientName", cc + tok + creds + `,"clientName":"Front"`, "", false, false, "connections.front.auth.clientName"},
		{"accept/clientSecretBasic", cc + tok + creds + `,"tokenEndpointAuthMethod":"client_secret_basic"`, "", true, true, ""},
		{"accept/clientSecretPost", cc + tok + creds + `,"tokenEndpointAuthMethod":"client_secret_post"`, "", true, true, ""},
		{"reject/authMethodNone", cc + tok + creds + `,"tokenEndpointAuthMethod":"none"`, "", false, false, "connections.front.auth.tokenEndpointAuthMethod"},
		{"accept/scopes", cc + tok + creds + `,"scopes":["read","write"]`, "", true, true, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := ccDoc(tt.auth, tt.transport)
			config.CheckCatalogCase(t, raw, tt.schema, tt.goValid, tt.path)
			if _, err := config.DecodeCatalog([]byte(raw)); err != nil && strings.Contains(err.Error(), "hidden-value") {
				t.Fatal(err)
			}
		})
	}
}

func TestClientCredentialsFields(t *testing.T) {
	c, err := config.DecodeCatalog([]byte(ccDoc("", "")))
	if err != nil {
		t.Fatal(err)
	}
	a := c.Connections["front"].Auth
	if a.Grant != config.GrantClientCredentials || a.TokenURL != "https://ws.example.invalid/oauth/token" || config.GrantAuthorizationCode != "authorization_code" {
		t.Fatal(a)
	}
	if refs := config.EnvRefs(c.Connections["front"]); !reflect.DeepEqual(refs, []string{"CC_ID", "CC_SECRET"}) {
		t.Fatal(refs)
	}
}

// grant and tokenUrl sit under auth, so changing either is an execution change.
func TestClientCredentialsExecutionChanged(t *testing.T) {
	c, err := config.DecodeCatalog([]byte(ccDoc("", "")))
	if err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*config.OAuth){
		"tokenUrl": func(a *config.OAuth) { a.TokenURL = "https://other.example.invalid/oauth/token" },
		"grant":    func(a *config.OAuth) { a.Grant = config.GrantAuthorizationCode },
	} {
		t.Run(name, func(t *testing.T) {
			a := c.Connections["front"]
			b := a
			auth := *a.Auth
			edit(&auth)
			b.Auth = &auth
			if !config.ExecutionChanged(a, b) {
				t.Fatal("not flagged")
			}
		})
	}
}
