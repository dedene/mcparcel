package ui

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

func TestAddPersonalStagesLocalAddDisabled(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	before := h.disk().Selections.Revision
	h.selectTab("Other")
	h.press("A")
	h.contains("MCParcel / Setup / Add personal connection", "> ID:", "Transport:   stdio   (Space switches)")
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
	h.contains("Added My Tool. It is off: press Space to turn it on.", "> My Tool", "1 unsaved change")
	h.press("ctrl+s")
	disk := h.disk()
	got := disk.Personal.Connections["my-tool"]
	if disk.Selections.Revision != before+1 || got.Label != "My Tool" || *got.Transport.Stdio.Command.Literal != "my-tool-fixture" || len(got.Transport.Stdio.Args) != 2 || disk.Selections.Connections["local:my-tool"].Enabled {
		t.Fatal(disk.Selections.Revision, got, disk.Selections.Connections["local:my-tool"])
	}

	web := newHarness(t, nil, 120, 35)
	web.press("a")
	web.typeText("web")
	web.formTo("transport")
	web.press("space")
	web.formTo("url")
	web.typeText("https://web.example/mcp")
	web.press("enter")
	if row := web.m.draft.Effective().Connections["local:web"]; row.Definition == nil || row.Definition.Transport.HTTP == nil {
		t.Fatalf("http add failed:\n%s", web.plain())
	}
}

func TestAddPersonalPrefillsDomain(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.selectTab("Design")
	h.press("a")
	if got := h.m.form.get("domains").value.String(); got != "design" {
		t.Fatal(got)
	}
	h.press("esc")
	if h.m.screen != screenList || h.m.form != nil || h.m.draft.Changes() != 0 {
		t.Fatal(h.m.screen)
	}
	h.selectTab("Other")
	h.press("a")
	if got := h.m.form.get("domains").value.String(); got != "" {
		t.Fatal(got)
	}
}

func TestEditPersonalPreservesUntouchedFields(t *testing.T) {
	h := newHarness(t, seedSecrets, 120, 35)
	before := h.m.draft.State().Personal.Connections["vault"]
	h.details("Other", "Vault")
	h.press("e")
	h.contains("> Label:       Vault_", "Command:     vault-fixture", "Arguments:   2 arguments (kept)", "Kept: cwd, env, inputs", "Transport:   stdio (read-only)")
	h.lacks("SECRET-MARKER", "ENV-MARKER", "fixture-cwd")
	h.press("ctrl+u")
	h.typeText("Vault 2")
	h.press("enter")
	if h.m.screen != screenDetails {
		t.Fatalf("form stayed open:\n%s", h.plain())
	}
	h.contains("MCParcel / Setup / Vault 2", "Updated Vault 2.")
	after := h.m.draft.State().Personal.Connections["vault"]
	before.Label = "Vault 2"
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("%+v\n%+v", before, after)
	}
}

func TestEditPersonalArgsAndURLReplaceOnly(t *testing.T) {
	h := newHarness(t, seedSecrets, 120, 35)
	h.details("Other", "Vault")
	h.press("e")
	h.formTo("args")
	h.contains("Arguments:   _")
	h.press("enter")
	if args := h.m.draft.State().Personal.Connections["vault"].Transport.Stdio.Args; len(args) != 2 || *args[1].Literal != "SECRET-MARKER" {
		t.Fatal(args)
	}
	h.press("e")
	h.formTo("args")
	h.typeText("--new value")
	h.press("enter")
	if args := h.m.draft.State().Personal.Connections["vault"].Transport.Stdio.Args; len(args) != 2 || *args[0].Literal != "--new" || *args[1].Literal != "value" {
		t.Fatal(args)
	}
	h.press("esc")
	h.details("Other", "Hook")
	h.press("e")
	h.contains("URL:         https://hook.example/... (kept)")
	h.press("enter")
	if u := h.m.draft.State().Personal.Connections["hook"].Transport.HTTP.URL.Literal; *u != "https://hook.example/private/path?q=QUERY-MARKER" {
		t.Fatal(*u)
	}
	h.press("e")
	h.formTo("url")
	h.typeText("https://hook2.example/mcp")
	h.press("enter")
	hook := h.m.draft.State().Personal.Connections["hook"]
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
	h.press("e")
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
	args := h.m.draft.State().Personal.Connections["files"].Transport.Stdio.Args
	if len(args) != 2 || args[1].Input == nil || args[1].Input.Input != "root" {
		t.Fatal(args)
	}
}

func TestEditPersonalExecutionChangeSetsReview(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.details("Other", "Excalidraw")
	h.press("e")
	h.formTo("command")
	h.typeText("excalidraw-v2")
	h.press("enter")
	h.contains("Updated Excalidraw. Review required: Space accepts it.", "Enabled: [!]   State: Review required")
	h.press("esc", "ctrl+s")
	sel := h.disk().Selections.Connections[idExcali]
	if !sel.Enabled || !sel.ReviewRequired {
		t.Fatal(sel)
	}
}

func spareDraft(t *testing.T) (*Draft, *config.Store) {
	t.Helper()
	store, _, state := fixtureStore(t, seedSecrets)
	d, err := NewDraft(state)
	if err != nil {
		t.Fatal(err)
	}
	return d, store
}

func TestDraftLocalUpdateAfterEnableKeepsReview(t *testing.T) {
	d, store := spareDraft(t)
	if err := d.SetEnabled("local:spare", true); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateLocal("local:spare", []byte(`{"id":"spare","label":"Spare","transport":{"type":"stdio","command":"spare-v2"}}`)); err != nil {
		t.Fatal(err)
	}
	if sel := d.State().Selections.Connections["local:spare"]; !sel.Enabled || !sel.ReviewRequired {
		t.Fatal(sel)
	}
	next, err := d.Save(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if sel := next.Selections.Connections["local:spare"]; !sel.Enabled || !sel.ReviewRequired {
		t.Fatal(sel)
	}
}

func TestEnableThenLocalUpdateAddingInputStillSaves(t *testing.T) {
	d, store := spareDraft(t)
	if err := d.SetEnabled("local:spare", true); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateLocal("local:spare", []byte(`{"id":"spare","label":"Spare","inputs":{"root":{"kind":"path","description":"Root"}},"transport":{"type":"stdio","command":"spare-fixture","args":[{"input":"root"}]}}`)); err != nil {
		t.Fatal(err)
	}
	if row := d.Effective().Connections["local:spare"]; !slices.Contains(row.Blockers, "config_required") {
		t.Fatal(row.Blockers)
	}
	next, err := d.Save(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if sel := next.Selections.Connections["local:spare"]; !sel.Enabled {
		t.Fatal(sel)
	}
	if _, ok := next.Personal.Connections["spare"].Inputs["root"]; !ok {
		t.Fatal(next.Personal.Connections["spare"])
	}
}

func TestPersonalInvalidIDInline(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.press("a")
	h.typeText("Bad ID")
	h.press("enter")
	h.contains("ID: use lowercase letters, digits and single hyphens.")
	h.formTo("id")
	h.typeText("ok-id")
	h.press("enter")
	h.contains("Invalid: connections.ok-id.transport.command")
	if h.m.screen != screenForm || h.m.draft.Changes() != 0 {
		t.Fatal(h.m.screen, h.m.draft.Changes())
	}
}

func TestPersonalDuplicateIDInline(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.press("a")
	h.typeText("excalidraw")
	h.formTo("command")
	h.typeText("other")
	h.press("enter")
	h.contains(`A personal connection "excalidraw" already exists.`)
	if h.m.screen != screenForm || h.m.draft.Changes() != 0 {
		t.Fatal(h.m.screen, h.m.draft.Changes())
	}
}
