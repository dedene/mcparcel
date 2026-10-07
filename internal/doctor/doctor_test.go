package doctor

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/output"
)

func TestOfflineReadyConfigAllOK(t *testing.T) {
	in := inputFor(t, docs{personal: stdioPaper, selections: enabledPaper})
	executable(t, mkdir(t, in.PATH), "paper-mcp")
	checks := Offline(in)
	for _, c := range checks {
		if c.Status != OK {
			t.Fatalf("not ok: %+v", c)
		}
	}
	got := ids(checks)
	wantIDs := []string{
		"runtime.mode", "runtime.version", "runtime.binary", "storage.dir state", "storage.dir data", "storage.dir runtime",
		"config.file config.json", "config.file personal.json", "config.file selections.json", "config.state", "config.summary",
		"config.connection local:paper", "prereq.command local:paper",
	}
	if !slices.Equal(got, wantIDs) {
		t.Fatalf("rows %v\nwant %v", got, wantIDs)
	}
	if s := Summarize(checks); s != (output.DoctorSummary{OK: len(checks)}) {
		t.Fatal(s)
	}
	if Mode(in) != "desktop" {
		t.Fatal(Mode(in))
	}
}

func TestRowsHaveStableOrder(t *testing.T) {
	in := inputFor(t, docs{
		personal:   `{"schemaVersion":1,"credentialProfiles":{"team":{}},"connections":{"b":{"credentialProfile":"team","transport":{"type":"stdio","command":"b","env":{"K":{"secret":"op://v/i/f"}}}},"a":{"transport":{"type":"http","url":"https://a.example.invalid/mcp"}}}}`,
		local:      `{"schemaVersion":1,"credentialProfiles":{"p":{"mode":"desktop","account":"acct"}}}`,
		selections: `{"schemaVersion":1,"revision":1,"connections":{"local:b":{"enabled":true,"credentialProfile":"p"},"local:a":{"enabled":true}}}`,
	})
	// Rows arrive in any order; Offline sorts them by the catalogue.
	checks := Offline(in)
	got := ids(checks)
	wantIDs := []string{
		"runtime.mode", "runtime.version", "runtime.binary", "storage.dir state", "storage.dir data", "storage.dir runtime",
		"config.file config.json", "config.file personal.json", "config.file selections.json", "config.state", "config.summary", "prereq.onepassword",
		"config.connection local:a", "prereq.command local:a",
		"config.connection local:b", "credentials.profile local:b", "credentials.reference local:b", "prereq.command local:b",
	}
	if !slices.Equal(got, wantIDs) {
		t.Fatalf("rows %v\nwant %v", got, wantIDs)
	}
	// Rows from another group (upgrade.go) land in their catalogue slot.
	extra := order(append(slices.Clone(checks), output.DoctorCheck{ID: "version.catalog", Subject: "team"}))
	if got := ids(extra); got[2] != "runtime.binary" || got[10] != "version.catalog team" {
		t.Fatalf("upgrade rows out of place: %v", got)
	}
	live := Live(context.Background(), Input{}, nil)
	if all := order(append([]output.DoctorCheck{live}, checks...)); all[len(all)-1].ID != "live.tools" {
		t.Fatal("live.tools must come last")
	}
}

func TestTargetLimitsConnectionRows(t *testing.T) {
	in := inputFor(t, docs{
		personal:   `{"schemaVersion":1,"connections":{"a":{"transport":{"type":"stdio","command":"a"}},"b":{"transport":{"type":"stdio","command":"b"}},"c":{"transport":{"type":"stdio","command":"c"}}}}`,
		selections: `{"schemaVersion":1,"revision":1,"connections":{"local:a":{"enabled":true},"local:b":{"enabled":true}}}`,
	})
	in.Target = "local:b"
	checks := Offline(in)
	for _, c := range checks {
		if strings.HasPrefix(c.Subject, "local:") && c.Subject != "local:b" {
			t.Fatalf("row for another connection: %+v", c)
		}
	}
	find(t, checks, "config.connection", "local:b")
	find(t, checks, "runtime.mode", "")
	// A named disabled connection is one skip row.
	in.Target = "local:c"
	checks = Offline(in)
	want(t, find(t, checks, "config.connection", "local:c"), Skip, "")
	absent(t, checks, "prereq.command")
}

func TestRowsNeverContainSecrets(t *testing.T) {
	d := docs{
		personal: `{"schemaVersion":1,"credentialProfiles":{"team":{}},"connections":{
			"std":{"credentialProfile":"team","transport":{"type":"stdio","command":"std","args":["SECRETMARK-ARG"],
				"env":{"TOKEN":{"secret":"op://Vault Name/SECRETMARK-ITEM/field"},"PLAIN":"SECRETMARK-ENVVALUE","FROMENV":{"secret":"env:API_TOKEN"}}}},
			"web":{"transport":{"type":"http","url":"https://secretmark-host.example.invalid/SECRETMARK-PATH?q=SECRETMARK-QUERY","headers":{"X-Plain":"SECRETMARK-HEADER"}},
				"auth":{"type":"oauth","clientId":"SECRETMARK-CLIENT"}}}}`,
		local:      `{"schemaVersion":1,"credentialProfiles":{"p":{"mode":"desktop-service-account","account":"SECRETMARK-ACCOUNT","bootstrapRef":"op://v/SECRETMARK-BOOT/f"}}}`,
		selections: `{"schemaVersion":1,"revision":1,"connections":{"local:std":{"enabled":true,"credentialProfile":"p"},"local:web":{"enabled":true}}}`,
	}
	in := inputFor(t, d)
	rows := Offline(in)
	rows = append(rows, Live(context.Background(), withTarget(in, "local:web"), func(context.Context) (output.ToolList, error) {
		return output.ToolList{}, output.NewError("auth_required", nil)
	}))
	hin := headlessInput(inputFor(t, d))
	rows = append(rows, Offline(hin)...)
	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if text := strings.ToLower(string(b)); strings.Contains(text, "secretmark") || strings.Contains(text, "op://") {
		t.Fatalf("secret marker in rows: %s", b)
	}
	// The rows that look at those fields did run.
	want(t, find(t, rows, "credentials.reference", "local:std"), Fail, "")
	find(t, rows, "credentials.env", "local:std")
	find(t, rows, "credentials.oauth", "local:web")
}

func withTarget(in Input, id string) Input {
	in.Target = id
	return in
}

func TestSummarizeCountsStatuses(t *testing.T) {
	s := Summarize([]output.DoctorCheck{{Status: OK}, {Status: Warn}, {Status: Fail}, {Status: Fail}, {Status: Skip}})
	if s != (output.DoctorSummary{OK: 1, Warn: 1, Fail: 2, Skip: 1}) {
		t.Fatal(s)
	}
	if Mode(Input{RuntimeErr: context.Canceled}) != "unknown" {
		t.Fatal("mode")
	}
}
