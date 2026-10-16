package ui

import (
	"reflect"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

func TestAddPersonalStagesLocalAddDisabled(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	before := h.disk().Selections.Revision
	h.selectTab("Other")
	h.press("n")
	h.contains("New personal connection", "> ID:", "Transport:   stdio   (Space switches)")
	h.typeText("my-tool")
	h.formTo("label")
	h.typeText("My Tool")
	h.formTo("command")
	h.typeText("my-tool-fixture")
	h.formTo("args")
	h.typeText("--port 3000")
	h.press("enter")
	if h.m.screen != screenList {
		t.Fatalf("form stayed open:\n%s", h.plain())
	}
	h.focused("[ ] My Tool")
	h.contains("Added My Tool. It is off: press Space to turn it on.")
	disk := h.disk()
	got := disk.Personal.Connections["my-tool"]
	if disk.Selections.Revision != before+1 || got.Label != "My Tool" || *got.Transport.Stdio.Command.Literal != "my-tool-fixture" || len(got.Transport.Stdio.Args) != 2 || disk.Selections.Connections["local:my-tool"].Enabled {
		t.Fatal(disk.Selections.Revision, got, disk.Selections.Connections["local:my-tool"])
	}

	web := newHarness(t, nil, 120, 35)
	web.press("n")
	web.typeText("web")
	web.formTo("transport")
	web.press("space")
	web.formTo("url")
	web.typeText("https://web.example/mcp")
	web.press("enter")
	if row := web.m.live.Effective().Connections["local:web"]; row.Definition == nil || row.Definition.Transport.HTTP == nil {
		t.Fatalf("http add failed:\n%s", web.plain())
	}
}

func TestAddPersonalPrefillsDomain(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.selectTab("Design")
	h.press("n")
	if got := h.m.form.get("domains").value.String(); got != "design" {
		t.Fatal(got)
	}
	revision := h.disk().Selections.Revision
	h.press("esc")
	if h.m.screen != screenList || h.m.form != nil || h.disk().Selections.Revision != revision {
		t.Fatal(h.m.screen)
	}
	h.selectTab("Other")
	h.press("n")
	if got := h.m.form.get("domains").value.String(); got != "" {
		t.Fatal(got)
	}
}

func TestEditPersonalPreservesUntouchedFields(t *testing.T) {
	h := newHarness(t, seedSecrets, 120, 35)
	before := h.m.live.State().Personal.Connections["vault"]
	h.details("Other", "Vault")
	h.do("Edit")
	h.contains("> Label:       Vault_", "Command:     vault-fixture", "Arguments:   2 arguments (kept)", "Kept: cwd, env, inputs", "Transport:   stdio (read-only)")
	h.lacks("SECRET-MARKER", "ENV-MARKER", "fixture-cwd")
	h.press("ctrl+u")
	h.typeText("Vault 2")
	h.press("enter")
	if h.m.screen != screenDetails {
		t.Fatalf("form stayed open:\n%s", h.plain())
	}
	h.contains("Other / Vault 2", "Updated Vault 2.")
	after := h.m.live.State().Personal.Connections["vault"]
	before.Label = "Vault 2"
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("%+v\n%+v", before, after)
	}
}

func TestEditPersonalArgsAndURLReplaceOnly(t *testing.T) {
	h := newHarness(t, seedSecrets, 120, 35)
	h.details("Other", "Vault")
	h.do("Edit")
	h.formTo("args")
	h.contains("Arguments:   _")
	h.press("enter")
	if args := h.m.live.State().Personal.Connections["vault"].Transport.Stdio.Args; len(args) != 2 || *args[1].Literal != "SECRET-MARKER" {
		t.Fatal(args)
	}
	h.do("Edit")
	h.formTo("args")
	h.typeText("--new value")
	h.press("enter")
	if args := h.m.live.State().Personal.Connections["vault"].Transport.Stdio.Args; len(args) != 2 || *args[0].Literal != "--new" || *args[1].Literal != "value" {
		t.Fatal(args)
	}
	h.press("esc")
	h.details("Other", "Hook")
	h.do("Edit")
	h.contains("URL:         https://hook.example/... (kept)")
	h.press("enter")
	if u := h.m.live.State().Personal.Connections["hook"].Transport.HTTP.URL.Literal; *u != "https://hook.example/private/path?q=QUERY-MARKER" {
		t.Fatal(*u)
	}
	h.do("Edit")
	h.formTo("url")
	h.typeText("https://hook2.example/mcp")
	h.press("enter")
	hook := h.m.live.State().Personal.Connections["hook"]
	if *hook.Transport.HTTP.URL.Literal != "https://hook2.example/mcp" || *hook.Transport.HTTP.Headers["X-Key"].Literal != "HEADER-MARKER" {
		t.Fatal(hook.Transport.HTTP)
	}
}

func TestEditPersonalNonLiteralArgsReadOnly(t *testing.T) {
	seed := func(t *testing.T, store *config.Store) {
		mustUpdate(t, store, func(s *config.State) {
			s.Personal.Connections["files"] = config.Connection{
				Label: "Files", Inputs: map[string]config.Input{"root": {Kind: "path", Description: "Root"}},
				Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal("files-fixture"), Args: []config.Value{config.Literal("--root"), {Input: &config.InputRef{Input: "root"}}}}},
			}
		})
	}
	h := newHarness(t, seed, 120, 35)
	h.details("Other", "Files")
	h.do("Edit")
	h.contains("Arguments:   2 arguments; edit with mcparcel local update")
	for range 5 {
		h.press("tab")
		h.typeText("x")
	}
	h.formTo("label")
	h.typeText("Files")
	h.press("enter")
	if h.m.screen != screenDetails {
		t.Fatalf("form stayed open:\n%s", h.plain())
	}
	args := h.m.live.State().Personal.Connections["files"].Transport.Stdio.Args
	if len(args) != 2 || args[1].Input == nil || args[1].Input.Input != "root" {
		t.Fatal(args)
	}
}

func TestEditPersonalExecutionChangeSetsReview(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.details("Other", "Excalidraw")
	h.do("Edit")
	h.formTo("command")
	h.typeText("excalidraw-v2")
	h.press("enter")
	h.contains("Updated Excalidraw. Review required: Space accepts it.", "[~] needs a review")
	sel := h.disk().Selections.Connections[idExcali]
	if !sel.Enabled || !sel.ReviewRequired {
		t.Fatal(sel)
	}
}

// A personal connection turned on, then edited so it runs differently,
// needs a review, as with local update after enable.
func TestLocalUpdateAfterEnableNeedsReview(t *testing.T) {
	h := newHarness(t, seedSecrets, 120, 35)
	h.details("Other", "Spare")
	h.press("space")
	if sel := h.disk().Selections.Connections["local:spare"]; !sel.Enabled || sel.ReviewRequired {
		t.Fatal(sel)
	}
	h.do("Edit")
	h.formTo("command")
	h.typeText("spare-v2")
	h.press("enter")
	if sel := h.disk().Selections.Connections["local:spare"]; !sel.Enabled || !sel.ReviewRequired {
		t.Fatal(sel)
	}
	h.press("space")
	if sel := h.disk().Selections.Connections["local:spare"]; !sel.Enabled || sel.ReviewRequired {
		t.Fatal("space did not accept the review", sel)
	}
}

// Edit and Remove are made on the definition setup shows: one changed
// elsewhere since is not overwritten or removed, and the form says so.
func TestPersonalChangedElsewhereIsKept(t *testing.T) {
	h := newHarness(t, seedSecrets, 120, 35)
	h.details("Other", "Spare")
	h.do("Edit")
	setElsewhere := func(text string) {
		mustUpdate(t, h.store, func(s *config.State) {
			c := s.Personal.Connections["spare"]
			c.Description = text
			s.Personal.Connections["spare"] = c
		})
	}
	setElsewhere("set elsewhere")
	h.formTo("command")
	h.typeText("spare-v2")
	h.press("enter")
	h.contains("This changed elsewhere meanwhile, so the change no longer applies.")
	if c := h.disk().Personal.Connections["spare"]; c.Description != "set elsewhere" || *c.Transport.Stdio.Command.Literal == "spare-v2" {
		t.Fatalf("%+v", c)
	}
	h.press("esc")
	if h.m.screen != screenDetails {
		t.Fatal(h.m.screen)
	}
	setElsewhere("set again elsewhere")
	h.do("Remove")
	h.contains("This changed elsewhere meanwhile, so the change no longer applies.")
	if _, ok := h.disk().Personal.Connections["spare"]; !ok {
		t.Fatal("removed a definition that changed elsewhere")
	}
}

func TestPersonalInvalidIDInline(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.press("n")
	h.typeText("Bad ID")
	h.press("enter")
	h.contains("ID: use lowercase letters, digits and single hyphens.")
	h.formTo("id")
	h.typeText("ok-id")
	h.press("enter")
	h.contains("Invalid: connections.ok-id.transport.command")
	if h.m.screen != screenForm || len(h.disk().Personal.Connections) != len(h.m.opened.Personal.Connections) {
		t.Fatal(h.m.screen, h.disk().Personal.Connections)
	}
}

func TestPersonalDuplicateIDInline(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.press("n")
	h.typeText("excalidraw")
	h.formTo("command")
	h.typeText("other")
	h.press("enter")
	h.contains(`A personal connection "excalidraw" already exists.`)
	if h.m.screen != screenForm || h.disk().Selections.Revision != h.m.opened.Selections.Revision {
		t.Fatal(h.m.screen, h.disk().Selections.Revision)
	}
}
