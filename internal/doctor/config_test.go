package doctor

import (
	"context"
	"errors"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

func TestConfigFileRowsPerDocument(t *testing.T) {
	in := inputFor(t, docs{personal: stdioPaper, selections: enabledPaper})
	_, fieldErr := config.DecodeSelections([]byte(`{"schemaVersion":1,"revision":1,"connections":{},"extra":1}`))
	in.Files = config.FileReport{Files: []config.FileStatus{
		{Name: "config.json"},
		{Name: "personal.json", Present: true, Err: config.ErrUnsafePath},
		{Name: "selections.json", Present: true, Err: fieldErr},
		{Name: "catalog team", Err: errors.Join(config.ErrConfig, errors.New("x"))},
	}}
	in.Snapshot = nil
	checks := Offline(in)
	c := find(t, checks, "config.file", "config.json")
	if c.Status != OK || c.Message != "Not present." {
		t.Fatal(c)
	}
	want(t, find(t, checks, "config.file", "personal.json"), Fail, "unsafe_local_path")
	c = find(t, checks, "config.file", "selections.json")
	want(t, c, Fail, "invalid_config")
	if c.Message != config.FieldReason(fieldErr)+"." || c.Message == "." {
		t.Fatal(c.Message)
	}
	want(t, find(t, checks, "config.file", "catalog team"), Fail, "invalid_config")
	want(t, find(t, checks, "config.state", ""), Skip, "")
	want(t, find(t, checks, "config.summary", ""), Skip, "")
	absent(t, checks, "config.connection")
}

func TestConfigStateCrossDocumentError(t *testing.T) {
	in := inputFor(t, docs{personal: stdioPaper, selections: enabledPaper})
	in.Files.State, in.Snapshot = nil, nil
	_, in.Files.StateErr = config.DecodeLocal([]byte(`{"schemaVersion":1,"aliases":{"BAD":"x"}}`))
	if config.FieldReason(in.Files.StateErr) == "" {
		t.Fatal("fixture is not a field error")
	}
	c := find(t, Offline(in), "config.state", "")
	want(t, c, Fail, "invalid_config")
	if c.Message != config.FieldReason(in.Files.StateErr)+"." {
		t.Fatal(c.Message)
	}
	in = inputFor(t, docs{personal: stdioPaper, selections: enabledPaper})
	if c = find(t, Offline(in), "config.state", ""); c.Status != OK || c.Message != "The documents agree." {
		t.Fatal(c)
	}
}

func TestConfigLockHeldRow(t *testing.T) {
	in := inputFor(t, docs{personal: stdioPaper, selections: enabledPaper})
	in.Files, in.Snapshot, in.FilesErr = config.FileReport{}, nil, context.DeadlineExceeded
	checks := Offline(in)
	c := find(t, checks, "config.file", "-")
	want(t, c, Fail, "config_conflict")
	if c.Message != "Another mcparcel command holds the configuration lock." || c.NextAction != "Run doctor again when it has finished." {
		t.Fatal(c)
	}
	want(t, find(t, checks, "config.state", ""), Skip, "")
	want(t, find(t, checks, "config.summary", ""), Skip, "")
	in.FilesErr = config.ErrUnsafePath
	want(t, find(t, Offline(in), "config.file", "-"), Fail, "unsafe_local_path")
}

func TestConnectionRows(t *testing.T) {
	d := docs{
		personal: `{"schemaVersion":1,"credentialProfiles":{"team":{}},"connections":{
			"ready":{"transport":{"type":"stdio","command":"/bin/sh"}},
			"review":{"transport":{"type":"stdio","command":"/bin/sh"}},
			"needs":{"credentialProfile":"team","inputs":{"workspace":{"kind":"string","description":"w"},"zone":{"kind":"string","description":"z","default":"eu"}},"transport":{"type":"stdio","command":"/bin/sh","args":[{"input":"workspace"}]}},
			"sse":{"transport":{"type":"http","url":"https://sse.example.invalid/mcp","mode":"sse"}},
			"off":{"transport":{"type":"stdio","command":"/bin/sh"}}}}`,
		selections: `{"schemaVersion":1,"revision":1,"connections":{
			"local:ready":{"enabled":true},"local:review":{"enabled":true,"reviewRequired":true},"local:needs":{"enabled":true},
			"local:sse":{"enabled":true},"local:gone":{"enabled":true},"local:off":{"enabled":false}}}`,
	}
	in := inputFor(t, d)
	checks := Offline(in)
	want(t, find(t, checks, "config.connection", "local:ready"), OK, "")
	c := find(t, checks, "config.connection", "local:review")
	want(t, c, Warn, "review_required")
	if c.NextAction != "Run mcparcel sync to see what changed, then mcparcel enable local:review." {
		t.Fatal(c.NextAction)
	}
	c = find(t, checks, "config.connection", "local:gone")
	want(t, c, Warn, "connection_unavailable")
	if c.NextAction != "Run mcparcel disable local:gone." {
		t.Fatal(c)
	}
	c = find(t, checks, "config.connection", "local:needs")
	want(t, c, Fail, "config_required")
	if c.Message != "Needs: input workspace, credential profile." || c.NextAction != "Run mcparcel config input set local:needs workspace <value>, then mcparcel config profile bind local:needs <profile>." {
		t.Fatal(c)
	}
	want(t, find(t, checks, "config.connection", "local:sse"), Fail, "runtime_unsupported")
	if len(filterSubject(checks, "local:off")) != 0 {
		t.Fatal("rows for a disabled connection that was not named")
	}
	if s := find(t, checks, "config.summary", ""); s.Message != "3 enabled, 1 disabled, 1 review required, 1 unavailable." {
		t.Fatal(s.Message)
	}
	// Named and disabled: one skip row.
	in.Target = "local:off"
	checks = Offline(in)
	want(t, find(t, checks, "config.connection", "local:off"), Skip, "")
	if len(filterSubject(checks, "local:off")) != 1 {
		t.Fatal(ids(checks))
	}
	// Headless: an op:// reference through a desktop profile is a
	// config.connection failure.
	h := headlessInput(inputFor(t, docs{
		personal:   `{"schemaVersion":1,"credentialProfiles":{"team":{}},"connections":{"op":{"credentialProfile":"team","transport":{"type":"stdio","command":"/bin/sh","env":{"K":{"secret":"op://v/i/f"}}}}}}`,
		local:      `{"schemaVersion":1,"credentialProfiles":{"p":{"mode":"desktop","account":"a"}}}`,
		selections: `{"schemaVersion":1,"revision":1,"connections":{"local:op":{"enabled":true,"credentialProfile":"p"}}}`,
	}))
	c = find(t, Offline(h), "config.connection", "local:op")
	want(t, c, Fail, "config_required")
	if c.Message != "This connection's 1Password profile uses the desktop app, which headless mode does not use." {
		t.Fatal(c.Message)
	}
}

func filterSubject(checks []output.DoctorCheck, subject string) []output.DoctorCheck {
	var out []output.DoctorCheck
	for _, c := range checks {
		if c.Subject == subject {
			out = append(out, c)
		}
	}
	return out
}

func TestNoConnectionsWarns(t *testing.T) {
	in := inputFor(t, docs{personal: stdioPaper, selections: `{"schemaVersion":1,"revision":1,"connections":{"local:paper":{"enabled":false}}}`})
	c := find(t, Offline(in), "config.summary", "")
	want(t, c, Warn, "")
	if c.Message != "No connections enabled." || c.NextAction != "Run mcparcel catalog, then mcparcel enable <mcp>." {
		t.Fatal(c)
	}
}
