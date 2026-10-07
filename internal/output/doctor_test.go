package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func doctorSample() DoctorData {
	return DoctorData{
		Version: "0.1.0-rc.2", Mode: "desktop",
		Checks: []DoctorCheck{
			{ID: "runtime.mode", Status: "ok", Message: "Desktop mode."},
			{ID: "runtime.version", Status: "fail", Message: "The runtime runs 0.1.0-rc.1 (PID 7); this CLI is 0.1.0-rc.2.", NextAction: "Run mcparcel runtime restart when no calls are active.", Code: "runtime_version_mismatch"},
			{ID: "prereq.command", Subject: "local:paper", Status: "ok", Message: "Found npx at /usr/bin/npx on the runtime's PATH."},
			{ID: "config.connection", Subject: "github:acmeco/pec#figma", Status: "warn", Message: "Review required.", NextAction: "Run mcparcel sync to see what changed, then mcparcel enable github:acmeco/pec#figma.", Code: "review_required"},
		},
		Summary: DoctorSummary{OK: 2, Warn: 1, Fail: 1},
	}
}

func TestDoctorJSONKeepsDataOnFailure(t *testing.T) {
	data := doctorSample()
	for _, v := range []any{data, &data} {
		var b bytes.Buffer
		if err := WriteJSON(&b, v, DoctorFailed(1, data.Checks[1])); err != nil {
			t.Fatal(err)
		}
		var env struct {
			OK    bool            `json:"ok"`
			Data  json.RawMessage `json:"data"`
			Error Error           `json:"error"`
		}
		if err := json.Unmarshal(b.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		var got DoctorData
		if env.OK || json.Unmarshal(env.Data, &got) != nil || len(got.Checks) != 4 || got.Summary.Fail != 1 {
			t.Fatal(b.String())
		}
		if env.Error.Code != "doctor_failed" || env.Error.Message != "1 check failed." || env.Error.NextAction != "Run mcparcel runtime restart when no calls are active." {
			t.Fatal(env.Error)
		}
	}
	// Other failures still drop data, and doctor_failed keeps only DoctorData.
	var b bytes.Buffer
	if err := WriteJSON(&b, data, NewError("internal_error", nil)); err != nil || !strings.Contains(b.String(), `"data":null`) {
		t.Fatal(b.String(), err)
	}
	b.Reset()
	if err := WriteJSON(&b, "x", DoctorFailed(2, DoctorCheck{})); err != nil || !strings.Contains(b.String(), `"data":null`) {
		t.Fatal(b.String(), err)
	}
	var ok bytes.Buffer
	if err := WriteJSON(&ok, data, nil); err != nil || !strings.Contains(ok.String(), `"checks":[{"id":"runtime.mode","status":"ok","message":"Desktop mode."}`) {
		t.Fatal(ok.String())
	}
}

func TestDoctorFailedExitCode8(t *testing.T) {
	e := DoctorFailed(3, DoctorCheck{})
	if ExitCode(e) != 8 || ExitCode(fmt.Errorf("wrapped: %w", e)) != 8 || e.Message != "3 checks failed." {
		t.Fatal(e)
	}
	if e.NextAction != "Fix the failed checks, starting with the first; each row has its next action." {
		t.Fatal(e.NextAction)
	}
}

func TestHumanDoctorTable(t *testing.T) {
	var b bytes.Buffer
	if err := WriteHuman(&b, doctorSample()); err != nil {
		t.Fatal(err)
	}
	golden := `Status  Check             Subject                 Message
ok      runtime.mode                              Desktop mode.
fail    runtime.version                           The runtime runs 0.1.0-rc.1 (PID 7); this CLI is 0.1.0-rc.2.
          next: Run mcparcel runtime restart when no calls are active.
ok      prereq.command    local:paper             Found npx at /usr/bin/npx on the runtime's PATH.
warn    config.connection github:acmeco/pec#figma Review required.
          next: Run mcparcel sync to see what changed, then mcparcel enable github:acmeco/pec#figma.
2 ok, 1 warn, 1 fail, 0 skip.
`
	if b.String() != golden {
		t.Fatalf("got:\n%s\nwant:\n%s", b.String(), golden)
	}
	// A subject longer than the cap pushes its message right; nothing is cut.
	long := strings.Repeat("x", 50)
	data := DoctorData{Checks: []DoctorCheck{{ID: "a", Subject: long, Status: "ok", Message: "M."}, {ID: "b", Subject: "s", Status: "ok", Message: "N."}}}
	b.Reset()
	if err := WriteHuman(&b, &data); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(b.String(), "\n")
	if !strings.HasSuffix(lines[1], long+" M.") || lines[2] != "ok      b     s"+strings.Repeat(" ", 40)+"N." {
		t.Fatalf("%q", lines)
	}
	if err := WriteHuman(&b, (*DoctorData)(nil)); err == nil {
		t.Fatal("nil data")
	}
}

func TestHumanDoctorCleansControlCharacters(t *testing.T) {
	data := DoctorData{Checks: []DoctorCheck{{ID: "runtime.version", Subject: "sub\x1b[2J", Status: "fail", Message: "runs 1.0\x1b[31m\u202e", NextAction: "next\r\nline"}}}
	var b bytes.Buffer
	if err := WriteHuman(&b, data); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.ContainsAny(out, "\x1b\r\u202e") || strings.Count(out, "\n") != 4 {
		t.Fatalf("%q", out)
	}
}
