package output

import (
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

func metadataRows() []config.EffectiveConnection {
	return []config.EffectiveConnection{
		{ID: "github:acme/catalog#context7", Available: true, Enabled: true, Definition: &config.Connection{Label: "Context7"}},
		{ID: "local:echo", Available: true, Definition: &config.Connection{Label: "echo"}},
	}
}

// TestMetadataTableAndPipe: a terminal gets a header row, aligned columns
// and the sources; a pipe gets tab-separated rows only, and neither prints
// the revision.
func TestMetadataTableAndPipe(t *testing.T) {
	source := SourceMetadata{Source: config.Source{Owner: "acme", Repo: "catalog", Path: "mcparcel.json", Commit: strings.Repeat("a", 40)}, CacheAgeSeconds: 12.7}
	data := MetadataData{Revision: 9, Items: metadataRows(), Sources: []SourceMetadata{source}, Available: 2}
	pipe := "github:acme/catalog#context7\tContext7\tReady\nlocal:echo\techo\tDisabled\n"
	if got := humanMetadata(data); got != pipe {
		t.Fatalf("pipe:\n%q", got)
	}
	data.Table = true
	table := "" +
		"ID                            Label     State\n" +
		"github:acme/catalog#context7  Context7  Ready\n" +
		"local:echo                    echo      Disabled\n" +
		"\n" +
		"Source: acme/catalog mcparcel.json @ " + strings.Repeat("a", 40) + " (snapshot age 12s)\n"
	if got := humanMetadata(data); got != table {
		t.Fatalf("table:\n%s", got)
	}
}

func TestMetadataEmptyStates(t *testing.T) {
	for _, tc := range []struct {
		data MetadataData
		want string
	}{
		{MetadataData{List: true}, "No connections yet. Run mcparcel setup, or mcparcel add <owner/repo>.\n"},
		{MetadataData{}, "No connections yet. Run mcparcel setup, or mcparcel add <owner/repo>.\n"},
		{MetadataData{List: true, Available: 1}, "Nothing is on. 1 connection is available. Run mcparcel catalog to see it.\n"},
		{MetadataData{List: true, Available: 3}, "Nothing is on. 3 connections are available. Run mcparcel catalog to see them.\n"},
		{MetadataData{Domain: "docs", Available: 3}, "No connections in domain docs. Run mcparcel catalog to see all of them.\n"},
	} {
		if got := EmptyMetadata(tc.data); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.data, got, tc.want)
		}
		// On a terminal the sentence is the output; a pipe gets rows only,
		// so with none stdout stays empty (the command puts it on stderr).
		tc.data.Table = true
		if got := humanMetadata(tc.data); got != tc.want {
			t.Errorf("table %+v: %q, want %q", tc.data, got, tc.want)
		}
		tc.data.Table = false
		if got := humanMetadata(tc.data); got != "" {
			t.Errorf("pipe %+v: %q", tc.data, got)
		}
	}
}

// A table wider than the terminal cuts labels, so each row keeps ID and
// State on one line.
func TestMetadataTableFitsTerminalWidth(t *testing.T) {
	long := strings.Repeat("A long label ", 5)[:60]
	row := func(id, label string, enabled bool) config.EffectiveConnection {
		return config.EffectiveConnection{ID: id, Available: true, Enabled: enabled, Definition: &config.Connection{Label: label}}
	}
	data := MetadataData{Table: true, Width: 60, Items: []config.EffectiveConnection{row("local:echo", long, true), row("local:paper", "Paper", false)}}
	got := humanMetadata(data)
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "ID ") || !strings.HasSuffix(lines[0], "State") {
		t.Fatalf("%q", got)
	}
	for _, line := range lines {
		if len(line) > 60 {
			t.Errorf("line of %d columns: %q", len(line), line)
		}
	}
	if !strings.HasSuffix(lines[1], "...  Ready") || !strings.HasPrefix(lines[1], "local:echo ") || !strings.HasSuffix(lines[2], "Disabled") {
		t.Fatalf("%q", got)
	}
	// Width unknown (0) or wide enough: nothing is cut.
	for _, width := range []int{0, 200} {
		data.Width = width
		if got := humanMetadata(data); !strings.Contains(got, long+"  Ready") {
			t.Errorf("width %d cut a label: %q", width, got)
		}
	}
}

func TestSourceMutationHuman(t *testing.T) {
	data := SourceMutationData{Revision: 3, Source: config.Source{Owner: "acme", Repo: "catalog", Commit: "abcdef0123456789"}, Changed: true}
	if got := humanSourceMutation(data); got != "Added acme/catalog (abcdef0). Run mcparcel setup to choose its connections.\n" {
		t.Fatal(got)
	}
	data.Changed = false
	if got := humanSourceMutation(data); got != "acme/catalog is already added (abcdef0). Run mcparcel sync to update it.\n" {
		t.Fatal(got)
	}
}

// TestInspectHumanLabelsNoSecrets: inspect prints "Label: value" lines with
// groups indented, and says what kind each value is without printing it: no
// argument, env or header value, URL path or query, secret reference or
// input value.
func TestInspectHumanLabelsNoSecrets(t *testing.T) {
	allow := []string{"read", "write"}
	stdio := &config.Connection{
		Label: "Echo", Description: "Echo tool.", Domains: []string{"docs"},
		Inputs:            map[string]config.Input{"region": {Kind: "string"}, "token": {Kind: "string", Default: ptr("DEFAULT-CANARY")}, "org": {Kind: "string"}},
		CredentialProfile: "team",
		Transport: config.Transport{Stdio: &config.Stdio{
			Command: config.Literal("npx"),
			Args:    []config.Value{config.Literal("ARG-CANARY"), {Input: &config.InputRef{Input: "region"}}},
			Env: map[string]config.Value{
				"API_KEY": {Secret: &config.SecretRef{Secret: "op://Vault/OP-CANARY/token"}},
				"DEBUG":   config.Literal("ENV-CANARY"),
				"TOKEN":   {Secret: &config.SecretRef{Secret: "env:MY_TOKEN"}},
			},
			Cwd: ptrValue(config.Literal("/CWD-CANARY")),
		}},
		ToolPolicy:  &config.ToolPolicy{Allow: &allow, Deny: []string{"delete"}},
		Lifecycle:   &config.Lifecycle{IdleTimeout: "10m"},
		CallTimeout: "30s",
	}
	data := InspectData{
		Revision:  7,
		Item:      config.EffectiveConnection{ID: "local:echo", Available: true, Enabled: true, Definition: stdio, Blockers: []string{}, Policy: config.ToolPolicy{Deny: []string{"everything"}}},
		Selection: config.Selection{Enabled: true, Inputs: map[string]string{"region": "INPUT-CANARY"}, CredentialProfile: "team", DisabledTools: []string{"delete", "sk-SECRET CANARY"}},
	}
	want := "" +
		"ID: local:echo\n" +
		"Label: Echo\n" +
		"Description: Echo tool.\n" +
		"Source: Personal\n" +
		"State: Ready\n" +
		"Enabled: yes\n" +
		"Blockers: none\n" +
		"Domains: docs\n" +
		"Transport: stdio, npx, 2 arguments\n" +
		"Working directory: set (value hidden)\n" +
		"Environment:\n" +
		"  API_KEY: from 1Password\n" +
		"  DEBUG: set (value hidden)\n" +
		"  TOKEN: from environment variable MY_TOKEN\n" +
		"Inputs:\n" +
		"  org: string, not set\n" +
		"  region: string, set\n" +
		"  token: string, default\n" +
		"Credential profile: team, linked to team\n" +
		"Tool policy:\n" +
		"  Allow: read, write\n" +
		"  Deny: delete\n" +
		"Tools off: delete, 1 other\n" +
		"Idle timeout: 10m\n" +
		"Call timeout: 30s\n"
	got := humanInspect(data)
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	http := &config.Connection{
		Label: "Remote",
		Transport: config.Transport{HTTP: &config.HTTP{
			URL:     config.Literal("https://mcp.example.com/PATH-CANARY?q=QUERY-CANARY"),
			Headers: map[string]config.Value{"Authorization": {Secret: &config.SecretRef{Secret: "op://Vault/HDR-CANARY/token", Prefix: "Bearer "}}},
		}},
		Auth: &config.OAuth{Type: "oauth", Grant: config.GrantClientCredentials, TokenURL: "https://auth.example.com/TOKEN-CANARY", Scopes: []string{"read"}, ClientID: ptrValue(config.Literal("CLIENT-CANARY")), ClientSecret: &config.Value{Secret: &config.SecretRef{Secret: "op://Vault/SECRET-CANARY/s"}}},
	}
	data = InspectData{Item: config.EffectiveConnection{ID: "github:acme/catalog#remote", Available: true, Definition: http, Blockers: []string{"connection_disabled"}}, Source: &SourceMetadata{Source: config.Source{Owner: "acme", Repo: "catalog", Commit: strings.Repeat("b", 40)}}}
	got = humanInspect(data)
	for _, line := range []string{"Source: acme/catalog\n", "Catalog commit: " + strings.Repeat("b", 40) + "\n", "Transport: HTTP, mcp.example.com\n", "Headers:\n  Authorization: from 1Password\n", "Sign-in: OAuth, client credentials\n  Scopes: read\n  Client ID: set (value hidden)\n  Client secret: from 1Password\n"} {
		if !strings.Contains(got, line) {
			t.Errorf("missing %q in:\n%s", line, got)
		}
	}
	for _, out := range []string{humanInspect(data), want} {
		for _, canary := range []string{"CANARY", "op://", "Revision", "{"} {
			if strings.Contains(out, canary) {
				t.Errorf("inspect printed %q:\n%s", canary, out)
			}
		}
	}
}

func ptr(s string) *string                  { return &s }
func ptrValue(v config.Value) *config.Value { return &v }
