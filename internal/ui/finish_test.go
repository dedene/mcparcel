package ui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
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

// seedFinish adds personal connections the finish step treats differently,
// all off: two OAuth servers (Calendar, Mail), an HTTP server without OAuth
// metadata (Search) and two local commands (Maps, Wiki).
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
	})
}

func finishHarness(t *testing.T, seed func(*testing.T, *config.Store), w, ht int, opts ...func(*Options)) *harness {
	t.Helper()
	h := newHarness(t, seed, w, ht, append([]func(*Options){fixedNow()}, opts...)...)
	h.loads.items = fixtureTools
	h.loads.errs = map[string]error{}
	h.fin.needs = map[string]SignInState{}
	return h
}

// turnOn turns the labeled rows of the Other tab on and saves, which offers
// the finish step.
func (h *harness) turnOn(labels ...string) {
	h.t.Helper()
	h.selectTab("Other")
	for _, label := range labels {
		h.selectRow(label)
		h.press("space")
	}
	h.press("ctrl+s")
	if h.m.screen != screenFinish || h.m.finish.started {
		h.t.Fatalf("no finish offer:\n%s", h.plain())
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

// firstMsg runs cmd up to its first message: the first command of a batch.
func firstMsg(cmd tea.Cmd) tea.Msg {
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		return firstMsg(batch[0])
	}
	return msg
}

func (h *harness) rowState(id string) finishState {
	h.t.Helper()
	for _, r := range h.m.finish.rows {
		if r.id == id {
			return r.state
		}
	}
	h.t.Fatalf("no finish row %s", id)
	return 0
}

// The offer lists what each connection involves and contacts nothing; Esc
// leaves it for later, and setup then leaves no outcome.
func TestFinishOfferContactsNothing(t *testing.T) {
	h := finishHarness(t, seedFinish, 120, 35)
	h.turnOn("Wiki", "Calendar")
	h.contains("MCParcel / Setup / Finish", "Start 2 servers now?", "Enter start   Esc later")
	h.lineHas("Calendar", "sign-in")
	h.lineHas("Wiki", "wiki-fixture")
	if checks, signIns, _ := h.fin.counts(); h.loads.calls != 0 || checks != 0 || signIns != 0 {
		t.Fatal(h.loads.calls, checks, signIns)
	}
	h.press("esc")
	if h.m.screen != screenList || h.loads.calls != 0 {
		t.Fatal(h.m.screen, h.loads.calls)
	}
	// Declined connections are offered again with the next save.
	h.selectRow("Maps")
	h.press("space", "ctrl+s")
	h.contains("Start 3 servers now?")
	h.press("esc", "q")
	if r := h.m.Result(); !h.quit || r.Started != nil || r.SignIn != nil || r.Failed != nil {
		t.Fatal(h.quit, r)
	}
	// A save that turns nothing on, or no runtime to start, offers nothing.
	off := finishHarness(t, seedFinish, 120, 35)
	off.selectTab("Other")
	off.selectRow("Excalidraw")
	off.press("space", "ctrl+s")
	none := finishHarness(t, seedFinish, 120, 35, func(o *Options) { o.LoadTools = nil })
	none.selectTab("Other")
	none.selectRow("Wiki")
	none.press("space", "ctrl+s")
	if off.m.screen != screenList || none.m.screen != screenList {
		t.Fatal(off.m.screen, none.m.screen)
	}
}

// Enter starts finishLimit connections at once; each slot that frees up
// starts the next one, in label order, and the header counts what is ready.
func TestFinishStartsAtMostLimitAtOnce(t *testing.T) {
	h := finishHarness(t, seedFinish, 120, 35)
	most := 0
	h.loads.onLoad = func(string) { most = max(most, h.m.finish.count(finStarting)) }
	h.turnOn("Maps", "Wiki", "Search", "Calendar", "Mail")
	h.hold = true
	h.press("enter")
	if n := h.m.finish.count(finStarting); n != finishLimit || h.loads.calls != 0 {
		t.Fatal(n, h.loads.calls)
	}
	for _, id := range []string{idCalendar, idMail, idMaps} {
		if h.rowState(id) != finStarting {
			t.Fatal(id, "did not start first")
		}
	}
	h.lineHas("Calendar", "starting...")
	h.lineHas("Wiki", "waiting")
	h.contains("Finishing setup", "0 of 5 ready", "Esc stop")
	h.lacks("Up/Down") // every row fits: nothing scrolls
	h.flush()
	if most > finishLimit {
		t.Fatal("more than the limit ran at once:", most)
	}
	if want := []string{idCalendar, idMail, idMaps, idSearch, idWiki}; !slices.Equal(slices.Sorted(slices.Values(h.loads.ids)), want) {
		t.Fatal(h.loads.ids)
	}
	h.contains("5 of 5 ready", "Enter done")
	h.lineHas("Wiki", "[+]", "started - 2 tools")
	h.press("enter")
	if h.m.screen != screenList || h.m.finish != nil {
		t.Fatal(h.m.screen)
	}
	h.contains("5 of 5 ready.")
	// The loaded tools stay for the session, as after Connect.
	h.details("Other", "Wiki")
	h.contains("Schema: loaded this session less than 1m ago")
	if r := h.m.Result(); len(r.Started) != 5 || r.SignIn != nil || r.Failed != nil {
		t.Fatal(r)
	}
}

// Sign-ins line up in the order they are found and run one at a time, each
// on Enter: a connection whose stored sign-in is missing queues before it
// starts, one whose server asks for a sign-in queues after. s skips one.
func TestFinishSignInQueue(t *testing.T) {
	h := finishHarness(t, seedFinish, 120, 35)
	h.fin.needs[idCalendar] = SignInState{Possible: true, Needed: true}
	h.fin.needs[idMail] = SignInState{Possible: true, Needed: true}
	h.fin.needs[idSearch] = SignInState{Possible: true}
	h.loads.errs[idSearch] = output.NewError("auth_required", nil)
	h.fin.url = "https://auth.example/authorize?client_id=fixture&redirect_uri=http%3A%2F%2F127.0.0.1%3A49152%2Fcallback"
	h.turnOn("Calendar", "Mail", "Search", "Wiki")
	h.press("enter")
	if slices.Contains(h.loads.ids, idCalendar) || slices.Contains(h.loads.ids, idMail) {
		t.Fatal("a connection that needs a sign-in started first:", h.loads.ids)
	}
	h.lineHas("Calendar", "> ", "needs sign-in")
	h.lineHas("Mail", "queued for sign-in")
	h.lineHas("Search", "queued for sign-in")
	h.lineHas("Wiki", "started - 2 tools")
	h.contains("1 of 4 ready", "Enter sign in to Calendar   s skip   Esc stop")
	if _, signIns, _ := h.fin.counts(); signIns != 0 {
		t.Fatal("signed in before Enter")
	}

	// Enter opens the browser; the URL shows while the sign-in waits.
	h.fin.block = make(chan struct{})
	h.hold = true
	h.press("enter")
	msg := firstMsg(h.held[0])
	h.held = nil
	h.send(msg)
	h.contains("Opening your browser to sign in to Calendar. If it does not open, visit:", "https://auth.example/authorize?client_id=fixture")
	h.lineHas("Calendar", "waiting for browser sign-in...")
	h.contains("s skip sign-in   Esc stop")
	close(h.fin.block)
	h.flush()
	h.lineHas("Calendar", "signed in - 2 tools")
	h.lacks("https://auth.example")
	// Mail is next; s skips it, then Search signs in.
	h.lineHas("Mail", "> ", "needs sign-in")
	h.press("s")
	h.lineHas("Mail", "skipped")
	h.lineHas("Search", "> ", "needs sign-in")
	h.press("enter")
	h.lineHas("Search", "signed in - 2 tools")
	h.contains("3 of 4 ready", "Enter done")
	if _, _, signed := h.fin.counts(); !slices.Equal(signed, []string{idCalendar, idSearch}) {
		t.Fatal(signed)
	}
	h.press("enter")
	if r := h.m.Result(); !slices.Equal(r.Started, []string{idCalendar, idSearch, idWiki}) || !slices.Equal(r.SignIn, []string{idMail}) || r.Failed != nil {
		t.Fatal(r)
	}
}

// A failing connection never holds up the others; it shows why, r retries
// it, and a failed sign-in is a failure too.
func TestFinishFailureIsolated(t *testing.T) {
	h := finishHarness(t, seedFinish, 120, 35)
	h.loads.errs[idMaps] = output.NewError("connection_failed", nil)
	h.fin.needs[idCalendar] = SignInState{Possible: true, Needed: true}
	h.fin.signErr = map[string]error{idCalendar: output.NewError("auth_callback_unavailable", nil)}
	h.turnOn("Maps", "Wiki", "Calendar")
	h.press("enter")
	h.lineHas("Wiki", "started - 2 tools")
	h.lineHas("Maps", "[-]", output.NewError("connection_failed", nil).Message[:20])
	h.press("enter")
	h.lineHas("Calendar", "[-]", output.NewError("auth_callback_unavailable", nil).Message[:20])
	h.contains("1 of 3 ready", "r retry failed   Enter done")
	delete(h.loads.errs, idMaps)
	h.press("r")
	h.lineHas("Maps", "started - 2 tools")
	h.lineHas("Calendar", "needs sign-in") // checked again: it still needs one
	h.press("s", "enter")
	if r := h.m.Result(); !slices.Equal(r.Started, []string{idMaps, idWiki}) || !slices.Equal(r.SignIn, []string{idCalendar}) || r.Failed != nil {
		t.Fatal(r)
	}
}

// Without a sign-in here (headless) or for a server that cannot sign in,
// the line points at mcparcel auth instead of queueing.
func TestFinishWithoutSignInPointsAtAuth(t *testing.T) {
	h := finishHarness(t, seedFinish, 120, 35, func(o *Options) { o.SignIn = nil })
	h.fin.needs[idCalendar] = SignInState{Possible: true, Needed: true}
	h.loads.errs[idSearch] = output.NewError("auth_required", nil)
	h.turnOn("Calendar", "Search")
	h.press("enter")
	h.lineHas("Calendar", "run mcparcel auth local:calendar")
	h.lineHas("Search", "run mcparcel auth local:search")
	h.contains("0 of 2 ready", "Enter done")
	h.press("enter")
	if r := h.m.Result(); !slices.Equal(r.SignIn, []string{idCalendar, idSearch}) {
		t.Fatal(r)
	}
	// A 1Password failure is auth_required too, but no sign-in fixes it.
	op := finishHarness(t, seedFinish, 120, 35)
	op.loads.errs[idWiki] = output.NewError("auth_required", nil)
	op.turnOn("Wiki")
	op.press("enter")
	op.lineHas("Wiki", "run mcparcel auth local:wiki")
	if _, signIns, _ := op.fin.counts(); signIns != 0 {
		t.Fatal(signIns)
	}
}

// Esc stops the finish step: every running context is canceled, a running
// sign-in returns, and late results change nothing.
func TestFinishEscCancelsEverything(t *testing.T) {
	h := finishHarness(t, seedFinish, 120, 35)
	h.turnOn("Maps", "Wiki", "Search", "Mail")
	h.hold = true
	h.press("enter", "esc")
	if h.m.screen != screenList || h.m.finish != nil {
		t.Fatal(h.m.screen)
	}
	h.flush()
	if len(h.loads.canceled) != finishLimit || slices.Contains(h.loads.canceled, false) {
		t.Fatal("a load ran without being canceled", h.loads.canceled)
	}
	if r := h.m.Result(); r.Started != nil || r.Failed != nil || r.SignIn != nil {
		t.Fatal("stopped connections have an outcome", r)
	}

	// A sign-in waiting for the browser.
	s := finishHarness(t, seedFinish, 120, 35)
	s.fin.needs[idCalendar] = SignInState{Possible: true, Needed: true}
	s.fin.block = make(chan struct{})
	s.turnOn("Calendar")
	s.press("enter")
	s.hold = true
	s.press("enter")
	wait := s.held[0] // starts the sign-in goroutine
	s.held = nil
	done := make(chan tea.Msg, 1)
	go func() { done <- firstMsg(wait) }()
	s.press("esc")
	select {
	case err := <-s.fin.ended:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the sign-in did not return")
	}
	s.hold = false
	s.send(<-done) // stale: changes nothing
	if r := s.m.Result(); !slices.Equal(r.SignIn, []string{idCalendar}) || s.m.screen != screenList {
		t.Fatal(r, s.m.screen)
	}
	// Ctrl+C quits the same way.
	c := finishHarness(t, seedFinish, 120, 35)
	c.turnOn("Wiki")
	c.hold = true
	c.press("enter", "ctrl+c")
	if !c.quit && len(c.held) == 0 {
		t.Fatal("ctrl+c did not quit")
	}
	c.flush()
	if !c.quit || !slices.Equal(c.loads.canceled, []bool{true}) || !c.m.Result().Interrupted {
		t.Fatal(c.quit, c.loads.canceled)
	}
}

// The spinner ticks only while a connection starts or signs in; once all
// have settled the next tick schedules nothing. Without motion nothing ticks
// and the mark is the spinner's first frame, static.
func TestFinishTicksOnlyWhileWorking(t *testing.T) {
	h := finishHarness(t, seedFinish, 120, 35, func(o *Options) { o.Color, o.Unicode = true, true })
	h.fin.needs[idCalendar] = SignInState{Possible: true, Needed: true}
	h.turnOn("Wiki", "Calendar")
	h.ticks = nil
	h.hold = true
	h.press("enter")
	h.hold = false
	batch := h.held[0]().(tea.BatchMsg)
	h.held = nil
	h.run(batch[len(batch)-1]) // the spinner
	if len(h.ticks) != 1 || h.ticks[0].d != frameRate {
		t.Fatal(h.ticks)
	}
	frames := h.m.theme.spinner.Frames
	h.lineHas("Wiki", frames[0], "starting...")
	h.fireTick()
	h.lineHas("Wiki", frames[1], "starting...")
	for _, cmd := range batch[:len(batch)-1] {
		h.run(cmd)
	}
	h.lineHas("Calendar", "◐", "needs sign-in") // waits for the user: no spinner
	h.fireTick()
	if len(h.ticks) != 0 {
		t.Fatal("ticks while nothing runs", h.ticks)
	}
	h.press("enter") // the sign-in runs: ticking again
	if len(h.ticks) != 1 {
		t.Fatal(h.ticks)
	}

	still := finishHarness(t, seedFinish, 120, 35, func(o *Options) { o.Color, o.Unicode, o.ReduceMotion = true, true, true })
	still.turnOn("Wiki")
	still.ticks = nil
	still.hold = true
	still.press("enter")
	still.lineHas("Wiki", still.m.theme.spinner.Frames[0], "starting...")
	still.flush()
	still.lineHas("Wiki", "✓", "started · 2 tools")
	for _, tick := range still.ticks {
		if tick.d == frameRate {
			t.Fatal("a spinner tick without motion")
		}
	}
}

// Every finish screen fits every swept size, Unicode on and off.
func TestFinishFitsAllSizes(t *testing.T) {
	for _, unicode := range []bool{false, true} {
		h := finishHarness(t, seedFinish, 120, 35, func(o *Options) { o.Color, o.Unicode = true, unicode })
		h.fin.needs[idCalendar] = SignInState{Possible: true, Needed: true}
		h.fin.needs[idMail] = SignInState{Possible: true, Needed: true}
		h.fin.url = "https://auth.example/authorize?" + strings.Repeat("state=abcdef&", 20)
		h.loads.errs[idMaps] = output.NewError("connection_failed", nil)
		check := func(name string) {
			for _, w := range []int{40, 41, 59, 80, 99, 100, 120, 160} {
				for _, ht := range []int{10, 11, 17, 24, 29, 30, 35, 50} {
					h.send(resize(w, ht))
					checkFits(t, h, w, ht, name)
				}
			}
			h.send(resize(120, 35))
		}
		h.turnOn("Calendar", "Mail", "Maps", "Search", "Wiki")
		check("offer")
		h.press("enter")
		check("asks")
		h.fin.block = make(chan struct{})
		h.hold = true
		h.press("enter")
		msg := firstMsg(h.held[0])
		h.held = nil
		h.send(msg)
		check("sign-in URL")
		close(h.fin.block)
		h.flush()
		h.press("s")
		check("settled")
		h.send(resize(40, 10))
		h.press("down", "down", "down", "down", "down", "down")
		checkFits(t, h, 40, 10, "scrolled")
		h.contains("Wiki")
	}
}
