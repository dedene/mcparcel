package ui

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

var update = flag.Bool("update", false, "rewrite golden files")

// fakeLoader stands in for the runtime's tool listing. It records each
// call's connection and whether its context was already canceled.
type fakeLoader struct {
	calls    int
	ids      []string
	canceled []bool
	items    []string // raw JSON tool objects
	err      error
}

func (f *fakeLoader) load(ctx context.Context, id string) (output.ToolList, error) {
	f.calls++
	f.ids = append(f.ids, id)
	f.canceled = append(f.canceled, ctx.Err() != nil)
	if f.err != nil {
		return output.ToolList{}, f.err
	}
	list := output.ToolList{Connection: id}
	for _, item := range f.items {
		list.Items = append(list.Items, json.RawMessage(item))
	}
	return list, nil
}

// harness drives a Model without a terminal: keys go to Update, returned
// commands run synchronously and their messages are fed back.
type harness struct {
	t     *testing.T
	m     *Model
	store *config.Store
	paths config.Paths
	loads *fakeLoader
	held  []tea.Cmd
	hold  bool
	quit  bool
}

func newHarness(t *testing.T, seed func(*testing.T, *config.Store), w, h int, opts ...func(*Options)) *harness {
	t.Helper()
	store, paths, state := fixtureStore(t, seed)
	loads := &fakeLoader{}
	o := Options{Describe: describeFixture, LoadTools: loads.load}
	for _, apply := range opts {
		apply(&o)
	}
	m, err := NewModel(context.Background(), store, state, o)
	if err != nil {
		t.Fatal(err)
	}
	hr := &harness{t: t, m: m, store: store, paths: paths, loads: loads}
	hr.send(tea.WindowSizeMsg{Width: w, Height: h})
	return hr
}

func (h *harness) send(msg tea.Msg) {
	h.t.Helper()
	_, cmd := h.m.Update(msg)
	h.run(cmd)
}

func (h *harness) run(cmd tea.Cmd) {
	h.t.Helper()
	if cmd == nil {
		return
	}
	if h.hold {
		h.held = append(h.held, cmd)
		return
	}
	switch msg := cmd().(type) {
	case nil:
	case tea.QuitMsg:
		h.quit = true
	case tea.BatchMsg:
		for _, c := range msg {
			h.run(c)
		}
	default:
		h.send(msg)
	}
}

func (h *harness) flush() {
	h.t.Helper()
	h.hold = false
	held := h.held
	h.held = nil
	for _, cmd := range held {
		h.run(cmd)
	}
}

var namedKeys = map[string]tea.Key{
	"up": {Code: tea.KeyUp}, "down": {Code: tea.KeyDown}, "left": {Code: tea.KeyLeft}, "right": {Code: tea.KeyRight},
	"pgup": {Code: tea.KeyPgUp}, "pgdown": {Code: tea.KeyPgDown}, "home": {Code: tea.KeyHome}, "end": {Code: tea.KeyEnd},
	"space": {Code: tea.KeySpace, Text: " "}, "enter": {Code: tea.KeyEnter}, "tab": {Code: tea.KeyTab},
	"shift+tab": {Code: tea.KeyTab, Mod: tea.ModShift}, "esc": {Code: tea.KeyEscape}, "backspace": {Code: tea.KeyBackspace},
	"ctrl+c": {Code: 'c', Mod: tea.ModCtrl}, "ctrl+s": {Code: 's', Mod: tea.ModCtrl}, "ctrl+u": {Code: 'u', Mod: tea.ModCtrl},
}

func keyFor(t *testing.T, name string) tea.Key {
	t.Helper()
	if k, ok := namedKeys[name]; ok {
		return k
	}
	r, size := utf8.DecodeRuneInString(name)
	if size != len(name) {
		t.Fatalf("unknown key %q", name)
	}
	return tea.Key{Code: r, Text: name}
}

func (h *harness) press(keys ...string) {
	h.t.Helper()
	for _, name := range keys {
		h.send(tea.KeyPressMsg(keyFor(h.t, name)))
	}
}

func (h *harness) typeText(s string) {
	h.t.Helper()
	for _, r := range s {
		h.send(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
	}
}

func resize(w, h int) tea.WindowSizeMsg { return tea.WindowSizeMsg{Width: w, Height: h} }

func (h *harness) paste(s string) { h.t.Helper(); h.send(tea.PasteMsg{Content: s}) }

func (h *harness) view() string  { return h.m.View().Content }
func (h *harness) plain() string { return stripSGR(h.view()) }

func (h *harness) contains(want ...string) {
	h.t.Helper()
	for _, text := range want {
		if !strings.Contains(h.plain(), text) {
			h.t.Fatalf("view lacks %q:\n%s", text, h.plain())
		}
	}
}

func (h *harness) disk() config.State {
	h.t.Helper()
	state, err := h.store.Read(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return state
}

// fileBytes captures the config documents and every file under the config
// and data directories, so a test can prove nothing was written.
func (h *harness) fileBytes() map[string][]byte {
	h.t.Helper()
	out := map[string][]byte{}
	for _, root := range []string{h.paths.ConfigDir, h.paths.DataDir} {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				out[path+"/"] = nil
				return nil
			}
			data, err := os.ReadFile(path)
			out[path] = data
			return err
		})
		if err != nil {
			h.t.Fatal(err)
		}
	}
	return out
}

// selectRow moves the cursor to the row with the given label in the current tab.
func (h *harness) selectRow(label string) {
	h.t.Helper()
	rows := h.m.rows()
	for i, row := range rows {
		if rowLabel(row) == label {
			h.press("home")
			for range i {
				h.press("down")
			}
			return
		}
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, rowLabel(row))
	}
	sort.Strings(names)
	h.t.Fatalf("no row %q in %v", label, names)
}

// selectTab moves right until the named tab is current.
func (h *harness) selectTab(label string) {
	h.t.Helper()
	for range len(tabs(h.m.draft.Effective())) {
		if h.m.currentTab().label == label {
			return
		}
		h.press("right")
	}
	h.t.Fatalf("no tab %q", label)
}

// details opens the details of the row with label in tab.
func (h *harness) details(tab, label string) {
	h.t.Helper()
	h.selectTab(tab)
	h.selectRow(label)
	h.press("enter")
	if h.m.screen != screenDetails {
		h.t.Fatalf("details did not open:\n%s", h.plain())
	}
}

// formTo moves the form cursor to the field with key and clears it.
func (h *harness) formTo(key string) {
	h.t.Helper()
	for range len(h.m.form.visible()) {
		if h.m.form.current().key == key {
			h.press("ctrl+u")
			return
		}
		h.press("tab")
	}
	h.t.Fatalf("no form field %q", key)
}

// containsWrapped is contains for text a page may have wrapped over lines.
func (h *harness) containsWrapped(want ...string) {
	h.t.Helper()
	flat := strings.Join(strings.Fields(h.plain()), " ")
	for _, text := range want {
		if !strings.Contains(flat, text) {
			h.t.Fatalf("view lacks %q:\n%s", text, h.plain())
		}
	}
}

// lacks fails when the view shows any of the texts.
func (h *harness) lacks(texts ...string) {
	h.t.Helper()
	for _, text := range texts {
		if strings.Contains(h.plain(), text) {
			h.t.Fatalf("view shows %q:\n%s", text, h.plain())
		}
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Fatalf("%s differs; rerun with -update after checking:\n%s", path, got)
	}
}
