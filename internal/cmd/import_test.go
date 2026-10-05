package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

func TestImportParserSafety(t *testing.T) {
	for _, argv := range [][]string{{"import", "mcporter", "--file", "--json"}, {"import", "mcporter", "--file=a", "--file=b", "--json"}, {"import", "mcporter", "--file=a", "--bindings=", "--json"}, {"import", "mcporter", "--file=a", "--only=", "--json"}} {
		code, out, errOut := run(t, argv...)
		if code != 2 {
			t.Fatal(code, out, errOut)
		}
		if scanIntent(argv).json {
			if !strings.Contains(out, `"code":"invalid_arguments"`) || errOut != "" || strings.Count(out, "\n") != 1 {
				t.Fatal(out, errOut)
			}
		} else if out != "" || errOut == "" {
			t.Fatal(out, errOut)
		}
	}
	code, out, _ := run(t, "import", "mcporter", "--help")
	if code != 0 || !strings.Contains(out, "--only") || !strings.Contains(strings.Join(strings.Fields(out), " "), "--only paper --only context7") {
		t.Fatal(code, out)
	}
}

func blockedReport(t *testing.T) config.ImportReport {
	t.Helper()
	r, e := config.ImportMcporter([]byte(`{"mcpServers":{"bad":{"command":"fixture","env":{"API_KEY":"VALUE-CANARY"}}}}`), nil)
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func TestImportFailureDetails(t *testing.T) {
	report := blockedReport(t)
	for _, mode := range []bool{true, false} {
		var out, errOut bytes.Buffer
		code := writeFailure(&out, &errOut, mode, &config.ImportBlockedError{Report: report})
		if code != 2 || strings.Contains(out.String()+errOut.String(), "VALUE-CANARY") {
			t.Fatal(code, out.String(), errOut.String())
		}
		if mode {
			var e struct {
				Data  any
				Error struct {
					Details struct {
						Report config.ImportReport `json:"importReport"`
					}
				}
			}
			if err := json.Unmarshal(out.Bytes(), &e); err != nil || e.Data != nil || len(e.Error.Details.Report.Entries) != 1 || errOut.Len() != 0 {
				t.Fatal(out.String(), err)
			}
		} else if !strings.Contains(out.String(), "bad: blocked") || !strings.Contains(errOut.String(), "Selected import entries require changes.") {
			t.Fatal(out.String(), errOut.String())
		}
	}
}

func TestImportHumanEscapesNames(t *testing.T) {
	r, e := config.ImportMcporter([]byte(`{"mcpServers":{"bad\u001b\nname":{"command":"fixture","unknown\u001b\nkey":"VALUE-CANARY"}}}`), nil)
	if e != nil {
		t.Fatal(e)
	}
	text := renderImport(r)
	if strings.Contains(text, "\x1b") || strings.Contains(text, "bad\n") || strings.Contains(text, "VALUE-CANARY") || !strings.Contains(text, `bad\x1b\nname`) {
		t.Fatal(text)
	}
	raw, _ := json.Marshal(r)
	if !strings.Contains(string(raw), `bad\u001b\nname`) {
		t.Fatal(string(raw))
	}
}

func TestImportPreviewAbsentRoots(t *testing.T) {
	p := metadataEnv(t)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "MCPARCEL_RUNTIME_DIR"} {
		t.Setenv(key, p.Home+"/absent/"+key)
	}
	file := p.Home + "/source.json"
	if e := os.WriteFile(file, []byte(`{"mcpServers":{"fixture":{"command":"fixture"}}}`), 0o644); e != nil {
		t.Fatal(e)
	}
	code, out, errOut := run(t, "import", "mcporter", "--file", file, "--json")
	if code != 0 || errOut != "" || !strings.Contains(out, `"revision":0`) {
		t.Fatal(code, out, errOut)
	}
	if _, e := os.Stat(p.Home + "/absent"); !os.IsNotExist(e) {
		t.Fatal("preview created roots", e)
	}
}

func TestImportHumanSummarizesOmitted(t *testing.T) {
	report, err := config.ImportMcporter([]byte(`{"mcpServers":{"selected":{"command":"fixture","env":{"JAVA_HOME":"/fixture"}},"omitted":{"command":"fixture","env":{"API_KEY":"secret"},"args":["/fixture"]}}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range report.Entries {
		report.Entries[i].Selected = report.Entries[i].ID == "selected"
	}
	before, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	text := renderImport(report)
	if strings.Contains(text, "omitted:") || strings.Contains(text, "mcpServers.omitted") || !strings.Contains(text, "selected: applicable") || !strings.Contains(text, "warning local_value: mcpServers.selected.env.JAVA_HOME") || !strings.Contains(text, "1 selected, 1 applicable, 0 blocked, 1 omitted") {
		t.Fatal(text)
	}
	after, err := json.Marshal(report)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("human rendering changed JSON report", err)
	}
}
