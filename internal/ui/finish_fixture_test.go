package ui

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/config"
)

// fakeFinish stands in for the stored sign-in check and the browser
// sign-in. A sign-in runs on its own goroutine, so its fields are guarded.
type fakeFinish struct {
	mu      sync.Mutex
	checks  int
	signIns int
	signed  []string               // connections signed in to, in order
	needs   map[string]SignInState // what needsSignIn answers
	signErr map[string]error       // a failing sign-in
	url     string                 // passed to onURL; empty: none
	block   chan struct{}          // when set, a sign-in waits for it to close, or for its context
	loads   *fakeLoader            // a sign-in clears the connection's load error
	ended   chan error             // what each sign-in returned
}

func (f *fakeFinish) needsSignIn(_ context.Context, id string) SignInState {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	return f.needs[id]
}

func (f *fakeFinish) signIn(ctx context.Context, id string, onURL func(string, bool)) (err error) {
	defer func() { f.ended <- err }()
	f.mu.Lock()
	f.signIns++
	url, block, fail := f.url, f.block, f.signErr[id]
	f.mu.Unlock()
	if url != "" {
		onURL(url, true)
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if fail != nil {
		return fail
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signed = append(f.signed, id)
	delete(f.needs, id)
	delete(f.loads.errs, id)
	return nil
}

func (f *fakeFinish) counts() (checks, signIns int, signed []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks, f.signIns, slices.Clone(f.signed)
}

const (
	idCalendar = "local:calendar"
	idMail     = "local:mail"
	idSearch   = "local:search"
	idMaps     = "local:maps"
	idWiki     = "local:wiki"
)

// seedFinish adds personal connections setting up treats differently, all
// off: two OAuth servers (Calendar, Mail), an HTTP server without OAuth
// metadata (Search) and two local commands (Maps, Wiki). Excalidraw is off
// too, so s sets up only what a test turns on.
func seedFinish(t *testing.T, store *config.Store) {
	t.Helper()
	seedFixture(t, store)
	oauth := func(label, host string) config.Connection {
		return config.Connection{Label: label, Auth: &config.OAuth{Type: "oauth"}, Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal("https://" + host + "/mcp")}}}
	}
	mustUpdate(t, store, func(s *config.State) {
		s.Personal.Connections["calendar"] = oauth("Calendar", "calendar.example")
		s.Personal.Connections["mail"] = oauth("Mail", "mail.example")
		s.Personal.Connections["search"] = config.Connection{Label: "Search", Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal("https://search.example/mcp")}}}
		s.Personal.Connections["maps"] = config.Connection{Label: "Maps", Transport: stdio("maps-fixture")}
		s.Personal.Connections["wiki"] = config.Connection{Label: "Wiki", Transport: stdio("wiki-fixture")}
		s.Selections.Connections[idExcali] = config.Selection{}
	})
}

func finishHarness(t *testing.T, seed func(*testing.T, *config.Store), w, ht int, opts ...func(*Options)) *harness {
	t.Helper()
	h := newHarness(t, seed, w, ht, append([]func(*Options){fixedNow()}, opts...)...)
	h.loads.items = fixtureTools
	h.loads.errs = map[string]error{}
	h.fin.mu.Lock()
	h.fin.needs = map[string]SignInState{}
	h.fin.mu.Unlock()
	return h
}

// turnOn turns the labeled rows of the Other tab on; s then sets them up.
func (h *harness) turnOn(labels ...string) {
	h.t.Helper()
	h.selectTab("Other")
	for _, label := range labels {
		h.selectRow(label)
		h.press("space")
	}
	if h.loads.calls != 0 || h.m.screen != screenList {
		h.t.Fatalf("turning on started something:\n%s", h.plain())
	}
}

// rowLine is the screen line of the row labeled label (its column ends in
// the gutter), or "".
func (h *harness) rowLine(label string) string {
	for line := range strings.SplitSeq(h.plain(), "\n") {
		if strings.Contains(line, " "+label+"  ") {
			return line
		}
	}
	return ""
}

// lineHas fails unless the row labeled label shows each text.
func (h *harness) lineHas(label string, want ...string) {
	h.t.Helper()
	line := h.rowLine(label)
	for _, text := range want {
		if line == "" || !strings.Contains(line, text) {
			h.t.Fatalf("line %q lacks %q:\n%s", line, text, h.plain())
		}
	}
}

// stepUntil runs held commands one at a time, holding what they return,
// until done reports true. It never runs a command after that, so a test
// can stop while a sign-in waits for the browser.
func (h *harness) stepUntil(done func() bool) {
	h.t.Helper()
	for !done() {
		if len(h.held) == 0 {
			h.t.Fatalf("nothing left to run:\n%s", h.plain())
		}
		cmd := h.held[0]
		h.held = h.held[1:]
		switch msg := cmd().(type) {
		case nil:
		case tea.BatchMsg:
			h.held = append(h.held, msg...)
		case pendingTick:
			h.ticks = append(h.ticks, msg)
		case tea.QuitMsg:
			h.quit = true
		default:
			h.send(msg)
		}
	}
}

// stepFor is the step Result lists for id.
func stepFor(r Result, id string) (Step, bool) {
	i := slices.IndexFunc(r.Steps, func(s Step) bool { return s.ID == id })
	if i < 0 {
		return Step{}, false
	}
	return r.Steps[i], true
}

// signingIn reports whether the sign-in of id waits for the browser with
// its URL shown.
func (h *harness) signingIn(id string) func() bool {
	return func() bool {
		r := h.m.finish.current()
		return r != nil && r.id == id && (r.url != "" || h.fin.url == "")
	}
}
