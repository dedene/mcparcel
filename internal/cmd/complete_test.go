package cmd

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

// completionSeed has a personal context7 with a finite tool policy and one
// tool turned off, an off echo, a paper both personal and in a catalog (so
// only canonical IDs name it), the alias docs and the profiles work, work2 and 1p-team.
func completionSeed(t *testing.T) config.Paths {
	t.Helper()
	p := metadataEnv(t)
	source := config.Source{ID: "github-42", RepositoryID: 42, Owner: "fixture-owner", Repo: "fixture.repo", Path: "mcparcel.json", Ref: "main", Commit: strings.Repeat("a", 40)}
	stdio := func() config.Connection {
		return config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("nonexistent-fixture")}}}
	}
	_, err := config.NewStore(p).Update(context.Background(), 0, func(s *config.State) error {
		context7 := stdio()
		context7.ToolPolicy = &config.ToolPolicy{Allow: &[]string{"resolve-library-id", "query-docs", "bad name"}}
		s.Personal.Connections["context7"] = context7
		s.Personal.Connections["echo"] = stdio()
		s.Personal.Connections["paper"] = stdio()
		s.Selections.Connections["local:context7"] = config.Selection{Enabled: true, DisabledTools: []string{"query-docs"}}
		s.Selections.Connections["local:echo"] = config.Selection{}
		s.Selections.Connections["local:paper"] = config.Selection{Enabled: true}
		s.Local.Sources = []config.Source{source}
		s.Catalogs[source.ID] = config.Catalog{SchemaVersion: 1, Connections: map[string]config.Connection{"paper": stdio()}}
		s.Selections.Connections["github:fixture-owner/fixture.repo#paper"] = config.Selection{Enabled: true}
		s.Local.Aliases["docs"] = "local:context7"
		s.Local.CredentialProfiles["work"] = config.Profile{Mode: "desktop", Account: "Fixture"}
		s.Local.CredentialProfiles["work2"] = config.Profile{Mode: "desktop", Account: "Fixture"}
		s.Local.CredentialProfiles["1p-team"] = config.Profile{Mode: "desktop", Account: "Fixture"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func complete(t *testing.T, words ...string) []string {
	t.Helper()
	code, stdout, stderr := run(t, append([]string{completeCommand}, words...)...)
	if code != 0 || stderr != "" {
		t.Fatalf("%q: code=%d stderr=%q", words, code, stderr)
	}
	return strings.Fields(stdout)
}

func TestCompleteCandidates(t *testing.T) {
	completionSeed(t)
	canonical := "github:fixture-owner/fixture.repo#paper"
	for _, tc := range []struct {
		words []string
		want  []string
	}{
		{[]string{"to"}, []string{"tools"}},
		{[]string{"--json", "to"}, []string{"tools"}},
		{[]string{"au"}, []string{"auth"}},
		{[]string{"tools", ""}, []string{"context7", "disable", "docs", "enable", canonical, "list", "local:paper"}},
		{[]string{"tools", "list", ""}, []string{"context7", "docs", canonical, "local:paper"}},
		{[]string{"enable", ""}, []string{"echo"}},
		{[]string{"enable", "echo", "e"}, []string{"echo"}},
		{[]string{"disable", "do"}, []string{"docs"}},
		{[]string{"inspect", "e"}, []string{"echo"}},
		{[]string{"auth", "st"}, []string{"status"}},
		{[]string{"call", ""}, []string{"context7.", "docs.", canonical + ".", "local:paper."}},
		{[]string{"call", "context7."}, []string{"context7.", "context7.resolve-library-id"}},
		{[]string{"call", "docs.r"}, []string{"docs.resolve-library-id"}},
		{[]string{"call", "local:paper."}, []string{"local:paper."}},
		{[]string{"call", "github:fixture-owner/fix"}, []string{canonical + "."}},
		{[]string{"call", canonical + ".x"}, nil},
		{[]string{"call", "context7.resolve-library-id", ""}, nil},
		{[]string{"tools", "enable", "context7", ""}, []string{"query-docs"}},
		{[]string{"tools", "disable", "docs", ""}, []string{"resolve-library-id"}},
		{[]string{"remove", ""}, []string{"fixture-owner/fixture.repo"}},
		{[]string{"local", "remove", ""}, []string{"context7", "echo", "paper"}},
		// Profile IDs may hold digits and start with one.
		{[]string{"config", "profile", "bind", "context7", ""}, []string{"1p-team", "work", "work2"}},
		{[]string{"completion", ""}, []string{"bash", "fish", "zsh"}},
		{[]string{"help", "au"}, []string{"auth"}},
		{[]string{"help", "auth", ""}, []string{"lock", "logout", "status"}},
		{[]string{"help", "tools", "paper", ""}, nil},
		{[]string{"tools", "context7", ""}, nil},
		{[]string{"auth", "context7", ""}, nil},
		{[]string{"inspect", "context7", ""}, nil},
		{[]string{"call", "--args-file", ""}, nil},
		{[]string{"call", "--timeout=", "con"}, []string{"context7."}},
		{[]string{"call", "--timeout", "5s", "con"}, []string{"context7."}},
		{[]string{"call", "--", "-"}, nil},
		{[]string{"call", "--args="}, nil},
		{[]string{"add", ""}, nil},
		{[]string{"bogus", ""}, nil},
	} {
		t.Run(strings.Join(tc.words, " "), func(t *testing.T) {
			if got := complete(t, tc.words...); !slices.Equal(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCompleteCommandsAndFlags(t *testing.T) {
	metadataEnv(t)
	top := complete(t, "")
	for _, want := range []string{"tools", "call", "setup", "help", "completion", "version"} {
		if !slices.Contains(top, want) {
			t.Errorf("top level misses %s: %q", want, top)
		}
	}
	for _, hidden := range []string{"daemon", "signal-wait", completeCommand} {
		if slices.Contains(top, hidden) {
			t.Errorf("top level offers hidden %s", hidden)
		}
	}
	flags := complete(t, "call", "-")
	for _, want := range []string{"--args", "--args-file", "--timeout", "--json", "--no-input", "--help", "-h", "--version", "-V"} {
		if !slices.Contains(flags, want) {
			t.Errorf("call flags miss %s: %q", want, flags)
		}
	}
	if got := complete(t, "tools", "list", "x", "--"); !slices.Equal(got, []string{"--help", "--json", "--no-input", "--version"}) {
		t.Errorf("hidden --cached offered, or flags missing: %q", got)
	}
	// No configuration yet: commands still complete, connections are none.
	if got := complete(t, "tools", ""); !slices.Equal(got, []string{"disable", "enable", "list"}) {
		t.Errorf("tools without configuration: %q", got)
	}
}

// Completion reads the configuration and nothing else: it writes no file
// and starts no runtime.
func TestCompleteIsSideEffectFree(t *testing.T) {
	p := completionSeed(t)
	root := filepath.Dir(p.Home)
	before := tree(t, root)
	for _, words := range [][]string{{"call", ""}, {"call", "context7."}, {"tools", "enable", "context7", ""}, {"local", "remove", ""}} {
		complete(t, words...)
	}
	if after := tree(t, root); !slices.Equal(before, after) {
		t.Fatalf("completion changed files:\nbefore %q\nafter  %q", before, after)
	}
	noRuntime(t, p)
}

func tree(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entries = append(entries, path+" "+info.ModTime().String()+" "+info.Mode().String())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestCompletionScripts(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			code, stdout, stderr := run(t, "completion", shell)
			if code != 0 || stderr != "" || !strings.Contains(stdout, "mcparcel __complete") {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if code, out, _ := run(t, "completion", shell, "--json"); code != 0 || !strings.Contains(out, `"shell":"`+shell+`"`) || strings.Count(out, "\n") != 1 {
				t.Fatalf("json: code=%d %q", code, out)
			}
			// Syntax only, with each shell that is installed.
			bin, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s is not installed", shell)
			}
			script := filepath.Join(t.TempDir(), "mcparcel."+shell)
			if err := os.WriteFile(script, []byte(stdout), 0o600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(bin, "-n", script).CombinedOutput(); err != nil {
				t.Fatalf("%s -n: %v\n%s", shell, err, out)
			}
		})
	}
	code, stdout, stderr := run(t, "completion", "pwsh-CANARY")
	if code != 2 || stdout != "" || !strings.Contains(stderr, "Unknown shell: choose bash, zsh or fish.") || strings.Contains(stderr, "CANARY") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

// The bash script passes the words before the cursor, and without
// bash-completion still keeps a canonical ID whole and gives bash only the
// part after its last colon.
func TestBashCompletionScript(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	_, script, _ := run(t, "completion", "bash")
	dir := t.TempDir()
	stub := "#!/bin/sh\nprintf '%s|\\n' \"$@\" >> \"$LOG\"\n[ \"$3\" = call ] && printf 'github:acme/x.repo#paper.\\n'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "mcparcel"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c.bash"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "log")
	cmd := exec.Command(bash, "--norc", "--noprofile", "-c", `source "$DIR/c.bash"
COMP_LINE='mcparcel --json call github:acme/x'; COMP_POINT=${#COMP_LINE}; _mcparcel; printf '[%s]' "${COMPREPLY[@]}"
COMP_LINE='mcparcel tools '; COMP_POINT=${#COMP_LINE}; _mcparcel; printf '[%s]' "${COMPREPLY[@]}"`)
	cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin", "DIR=" + dir, "LOG=" + log}
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != "[acme/x.repo#paper.][]" {
		t.Fatalf("%v: %q", err, out)
	}
	calls, err := os.ReadFile(log)
	if want := "__complete|--json|call|github:acme/x|__complete|tools||"; err != nil || strings.ReplaceAll(string(calls), "\n", "") != want {
		t.Fatalf("calls %q, want %q", calls, want)
	}
}

// Bash 3.2 (macOS /bin/bash) has no compopt: the script registers with
// nospace and adds the space itself, so a call target's connection still
// takes no space after it and any other single candidate does.
func TestBashCompletionScriptWithoutCompopt(t *testing.T) {
	const bash = "/bin/bash"
	version, err := exec.Command(bash, "-c", `printf '%s' "${BASH_VERSINFO[0]}"`).Output()
	if err != nil {
		t.Skip("/bin/bash is not installed")
	}
	if string(version) != "3" {
		t.Skipf("/bin/bash is bash %s, which has compopt", version)
	}
	_, script, _ := run(t, "completion", "bash")
	dir := t.TempDir()
	stub := "#!/bin/sh\ncase \"$2\" in call) printf 'echo.\\n' ;; tools) printf 'list\\n' ;; esac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "mcparcel"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c.bash"), []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bash, "--norc", "--noprofile", "-c", `source "$DIR/c.bash"
complete -p mcparcel
COMP_LINE='mcparcel call ec'; COMP_POINT=${#COMP_LINE}; _mcparcel; printf '[%s]' "${COMPREPLY[@]}"
COMP_LINE='mcparcel tools li'; COMP_POINT=${#COMP_LINE}; _mcparcel; printf '[%s]' "${COMPREPLY[@]}"`)
	cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin", "DIR=" + dir}
	out, err := cmd.CombinedOutput()
	want := "complete -o default -o nospace -F _mcparcel mcparcel\n[echo.][list ]"
	if err != nil || string(out) != want {
		t.Fatalf("%v: %q, want %q", err, out, want)
	}
}
