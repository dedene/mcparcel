package ui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// s sets up the servers that are on, in place: no other screen opens, and
// the list shows each one's tools once it started. With none on, or no
// runtime to start them, it says so and contacts nothing.
func TestSetUpStartsWhatIsOn(t *testing.T) {
	empty := finishHarness(t, nil, 120, 35)
	empty.press("s")
	empty.contains("Nothing to set up: turn a server on with Space first.")
	h := finishHarness(t, seedFinish, 120, 35)
	h.press("s") // what is on (a review, an unavailable server) waits on the user
	h.contains("Nothing setup can do on its own: open a server to see what it needs.")
	h.turnOn("Wiki")
	h.lineHas("Wiki", "[x]", "starts on first use")
	h.press("s")
	if h.m.screen != screenList || h.m.finish != nil || !slices.Equal(h.loads.ids, []string{idWiki}) {
		t.Fatal(h.m.screen, h.loads.ids)
	}
	h.lineHas("Wiki", "[x]", "2 tools")
	h.contains("Wiki is ready.", "3 on - 2 need a step")
	h.press("s")
	h.contains("Nothing setup can do on its own")
	if h.loads.calls != 1 {
		t.Fatal("s started a server that is set up", h.loads.calls)
	}
	none := finishHarness(t, seedFinish, 120, 35, func(o *Options) { o.LoadTools = nil })
	none.turnOn("Wiki")
	none.press("s")
	none.contains("Setting up is not available here.")
	if none.m.finish != nil {
		t.Fatal("a run started")
	}
}

// s starts finishLimit servers at once; each slot that frees up starts the
// next one, in label order. Rows show their phase while they start, and
// the list stays usable.
func TestSetUpStartsAtMostLimitAtOnce(t *testing.T) {
	h := finishHarness(t, seedFinish, 120, 35)
	most := 0
	h.loads.onLoad = func(string) { most = max(most, h.m.finish.count(finStarting)) }
	h.turnOn("Maps", "Wiki", "Search", "Calendar", "Mail")
	h.hold = true
	h.press("s")
	if n := h.m.finish.count(finStarting); n != finishLimit || h.loads.calls != 0 {
		t.Fatal(n, h.loads.calls)
	}
	for _, id := range []string{idCalendar, idMail, idMaps} {
		if h.m.finish.row(id).state != finStarting {
			t.Fatal(id, "did not start first")
		}
	}
	h.lineHas("Calendar", "starting...")
	h.lineHas("Wiki", "waiting")
	h.contains("Setting up: 0 of 5 ready.", "q stop")
	h.lacks("servers need a step")
	h.press("down") // the list stays usable
	h.flush()
	if most > finishLimit {
		t.Fatal("more than the limit ran at once:", most)
	}
	if want := []string{idCalendar, idMail, idMaps, idSearch, idWiki}; !slices.Equal(slices.Sorted(slices.Values(h.loads.ids)), want) {
		t.Fatal(h.loads.ids)
	}
	h.contains("All 5 are ready.", "7 on - 2 need a step")
	h.lineHas("Wiki", "2 tools")
	// The loaded tools stay for the session: the tools page does not load
	// them again.
	calls := h.loads.calls
	h.details("Other", "Wiki")
	h.press("enter")
	h.contains("Loaded less than 1m ago (2 tools).")
	if h.loads.calls != calls {
		t.Fatal("the tools page loaded again")
	}
	if r := h.m.Result(); r.On != 7 || r.Ready != 5 || len(r.Steps) != 2 {
		t.Fatal(r)
	}
}

// Sign-ins line up in the order they are found and run one at a time, on
// their own: a server whose stored sign-in is missing queues before it
// starts, one whose server asks for a sign-in queues after. The bottom of
// the list says where to sign in, with the URL, and Esc skips one.
func TestSetUpSignsInOneAtATime(t *testing.T) {
	h := finishHarness(t, seedFinish, 120, 35)
	h.fin.needs[idCalendar] = SignInState{Possible: true, Needed: true}
	h.fin.needs[idMail] = SignInState{Possible: true, Needed: true}
	h.fin.needs[idSearch] = SignInState{Possible: true}
	h.loads.errs[idSearch] = output.NewError("auth_required", nil)
	h.fin.url = "https://auth.example/authorize?client_id=fixture&redirect_uri=http%3A%2F%2F127.0.0.1%3A49152%2Fcallback"
	h.turnOn("Calendar", "Mail", "Search", "Wiki")
	h.lineHas("Calendar", "[~]", "sign in")
	h.contains("6 on - 4 need a step", "4 servers need a step.  s set up all")
	h.fin.block = make(chan struct{})
	h.hold = true
	h.press("s")
	h.stepUntil(h.signingIn(idCalendar))
	if slices.Contains(h.loads.ids, idCalendar) || slices.Contains(h.loads.ids, idMail) {
		t.Fatal("a server that needs a sign-in started first:", h.loads.ids)
	}
	h.lineHas("Calendar", "waiting for your browser...")
	h.lineHas("Mail", "next sign-in")
	h.containsWrapped("Sign in to Calendar in your browser. Didn't open? https://auth.example/authorize?client_id=fixture", "Esc skip this one q stop")
	close(h.fin.block)
	h.flush()
	h.lineHas("Calendar", "[x]", "2 tools")
	h.lineHas("Search", "[x]", "2 tools")
	h.lineHas("Wiki", "2 tools")
	h.lacks("https://auth.example")
	if _, _, signed := h.fin.counts(); !slices.Equal(signed, []string{idCalendar, idMail, idSearch}) {
		t.Fatal(signed)
	}

	// Esc skips the sign-in that waits; the next one starts.
	skip := finishHarness(t, seedFinish, 120, 35)
	skip.fin.needs[idCalendar] = SignInState{Possible: true, Needed: true}
	skip.fin.needs[idMail] = SignInState{Possible: true, Needed: true}
	skip.turnOn("Calendar", "Mail")
	skip.fin.block = make(chan struct{})
	skip.hold = true
	skip.press("s")
	skip.stepUntil(skip.signingIn(idCalendar))
	skip.press("esc")
	skip.stepUntil(skip.signingIn(idMail))
	skip.contains("Calendar still needs a sign-in.", "Sign in to Mail in your browser.")
	skip.lineHas("Calendar", "[~]", "sign in")
	close(skip.fin.block)
	skip.flush()
	skip.lineHas("Mail", "2 tools")
	if r := skip.m.Result(); r.On != 4 || r.Ready != 1 || len(r.Steps) != 3 || r.Steps[0] != (Step{ID: idCalendar, Label: "Calendar", Need: NeedSignIn}) {
		t.Fatal(r)
	}
}

// q while setting up stops it: every running context is canceled, a
// running sign-in returns, late results change nothing and setup stays
// open. q again quits.
func TestSetUpQStopsThenQuits(t *testing.T) {
	h := finishHarness(t, seedFinish, 120, 35)
	h.turnOn("Maps", "Wiki", "Search", "Mail")
	h.hold = true
	h.press("s", "q")
	if h.m.finish != nil || h.quit {
		t.Fatal("q did not stop, or quit")
	}
	h.flush()
	if len(h.loads.canceled) != finishLimit || slices.Contains(h.loads.canceled, false) {
		t.Fatal("a load ran without being canceled", h.loads.canceled)
	}
	h.contains("Stopped. 0 of 4 ready.")
	h.lineHas("Wiki", "starts on first use")
	h.press("q")
	if !h.quit {
		t.Fatal("q again did not quit")
	}

	// A sign-in waiting for the browser.
	s := finishHarness(t, seedFinish, 120, 35)
	s.fin.needs[idCalendar] = SignInState{Possible: true, Needed: true}
	s.fin.block = make(chan struct{})
	s.turnOn("Calendar")
	s.hold = true
	s.press("s")
	s.stepUntil(s.signingIn(idCalendar))
	wait := s.held[0] // waits for the sign-in to end
	s.held = nil
	done := make(chan tea.Msg, 1)
	go func() { done <- wait() }()
	s.press("q")
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
	s.lineHas("Calendar", "sign in")
	if step, ok := stepFor(s.m.Result(), idCalendar); !ok || step.Need != NeedSignIn || s.quit {
		t.Fatal(step, s.quit)
	}

	// Ctrl+C quits at once, canceling the run.
	c := finishHarness(t, seedFinish, 120, 35)
	c.turnOn("Wiki")
	c.hold = true
	c.press("s", "ctrl+c")
	c.flush()
	if !c.quit || !slices.Equal(c.loads.canceled, []bool{true}) || !c.m.Result().Interrupted {
		t.Fatal(c.quit, c.loads.canceled)
	}
}

// A failing server never holds up the others; it shows why, and s tries it
// again (not the ones that started). A failed sign-in is a failure too.
func TestSetUpFailureIsolated(t *testing.T) {
	h := finishHarness(t, seedFinish, 120, 35)
	h.loads.errs[idMaps] = output.NewError("connection_failed", nil)
	h.fin.needs[idCalendar] = SignInState{Possible: true, Needed: true}
	h.fin.signErr = map[string]error{idCalendar: output.NewError("auth_callback_unavailable", nil)}
	h.turnOn("Maps", "Wiki", "Calendar")
	h.press("s")
	h.lineHas("Wiki", "2 tools")
	h.lineHas("Maps", "[-]", "could not start")
	h.lineHas("Calendar", "[-]", "sign-in port in use")
	h.contains("1 of 3 ready.", "5 on - 4 need a step")
	h.selectRow("Maps")
	h.paneContains("[-] could not start", "Could not start Maps.")
	h.paneLacks("[*] Could not start") // the state line says it
	delete(h.loads.errs, idMaps)
	delete(h.fin.signErr, idCalendar)
	calls := h.loads.calls
	h.press("s")
	h.lineHas("Maps", "2 tools")
	h.lineHas("Calendar", "2 tools")
	if got := h.loads.ids[calls:]; slices.Contains(got, idWiki) {
		t.Fatal("s started a server that started already:", got)
	}
	if r := h.m.Result(); r.Ready != 3 || len(r.Steps) != 2 {
		t.Fatal(r)
	}
}

// Without a sign-in here (headless) or for a server that cannot sign in,
// the row points at mcparcel auth instead of queueing.
func TestSetUpWithoutSignInPointsAtAuth(t *testing.T) {
	h := finishHarness(t, seedFinish, 120, 35, func(o *Options) { o.SignIn = nil })
	h.fin.needs[idCalendar] = SignInState{Possible: true, Needed: true}
	h.loads.errs[idSearch] = output.NewError("auth_required", nil)
	h.turnOn("Calendar", "Search")
	h.press("s")
	h.lineHas("Calendar", "run mcparcel auth calendar")
	h.lineHas("Search", "run mcparcel auth search")
	h.contains("0 of 2 ready.")
	for _, id := range []string{idCalendar, idSearch} {
		if step, ok := stepFor(h.m.Result(), id); !ok || step.Need != NeedSignIn {
			t.Fatal(id, step)
		}
	}
	// A 1Password failure is auth_required too, but no sign-in fixes it.
	op := finishHarness(t, seedFinish, 120, 35)
	op.loads.errs[idWiki] = output.NewError("auth_required", nil)
	op.turnOn("Wiki")
	op.press("s")
	op.lineHas("Wiki", "run mcparcel auth wiki")
	if _, signIns, _ := op.fin.counts(); signIns != 0 {
		t.Fatal(signIns)
	}
}

// s skips servers that wait on the user (an input, a missing command) and
// they keep their Status; s on a server page sets up just that server.
func TestSetUpSkipsBlockedAndServerPageSetsUpOne(t *testing.T) {
	h := finishHarness(t, seedFinish, 120, 35, func(o *Options) {
		o.CommandAvailable = func(command, _ string) bool { return command != "maps-fixture" }
	})
	mustUpdate(t, h.store, func(s *config.State) { s.Selections.Connections[idNotes] = config.Selection{Enabled: true} })
	h.turnOn("Maps", "Wiki", "Search")
	h.lineHas("Maps", "[-]", "maps-fixture not installed")
	h.press("s")
	if slices.Contains(h.loads.ids, idMaps) || slices.Contains(h.loads.ids, idNotes) {
		t.Fatal("s started a blocked server:", h.loads.ids)
	}
	h.lineHas("Maps", "not installed")
	h.selectTab("Research")
	h.lineHas("Notes", "[~]", "needs workspace")

	one := finishHarness(t, seedFinish, 120, 35)
	one.turnOn("Wiki", "Search")
	one.details("Other", "Wiki")
	one.press("s")
	if one.m.screen != screenDetails || !slices.Equal(one.loads.ids, []string{idWiki}) {
		t.Fatal(one.m.screen, one.loads.ids)
	}
	one.contains("Wiki is ready.", "[x] ready - 2 tools")
	one.press("esc")
	one.details("Other", "Maps")
	one.press("s")
	one.contains("Maps is off. Space turns it on.")
}

// The spinner ticks only while a server starts or signs in; once all have
// settled the next tick schedules nothing. Without motion nothing ticks and
// the mark is the spinner's first frame, static. A server waiting for a
// free slot never spins.
func TestSetUpTicksOnlyWhileWorking(t *testing.T) {
	h := finishHarness(t, seedFinish, 120, 35, func(o *Options) { o.Color, o.Unicode = true, true })
	h.turnOn("Wiki", "Maps", "Search", "Mail")
	h.ticks = nil
	h.hold = true
	h.press("s")
	h.hold = false
	batch := h.held[0]().(tea.BatchMsg)
	h.held = nil
	h.run(batch[len(batch)-1]) // the spinner
	if len(h.ticks) != 1 || h.ticks[0].d != frameRate {
		t.Fatal(h.ticks)
	}
	frames := h.m.theme.spinner.Frames
	h.lineHas("Mail", frames[0], "starting...")
	h.lineHas("Wiki", frames[0], "waiting")
	h.fireTick()
	h.lineHas("Mail", frames[1], "starting...")
	h.lineHas("Wiki", frames[0], "waiting")
	for _, cmd := range batch[:len(batch)-1] {
		h.run(cmd)
	}
	h.lineHas("Wiki", "●", "2 tools")
	h.fireTick()
	for _, tick := range h.ticks {
		if tick.d == frameRate {
			t.Fatal("ticks while nothing runs", h.ticks)
		}
	}

	still := finishHarness(t, seedFinish, 120, 35, func(o *Options) { o.Color, o.Unicode, o.ReduceMotion = true, true, true })
	still.turnOn("Wiki")
	still.ticks = nil
	still.hold = true
	still.press("s")
	still.lineHas("Wiki", still.m.theme.spinner.Frames[0], "starting...")
	still.flush()
	still.lineHas("Wiki", "●", "2 tools")
	for _, tick := range still.ticks {
		if tick.d == frameRate {
			t.Fatal("a spinner tick without motion")
		}
	}
}

// Every list fits every swept size while setting up runs, with a sign-in
// waiting and a URL longer than the screen, Unicode on and off; the URL
// shows whole where there is room.
func TestSetUpFitsAllSizes(t *testing.T) {
	url := "https://auth.example/authorize?response_type=code&client_id=" + strings.Repeat("c", 40) + "&state=" + strings.Repeat("s", 43) + "&end=TAIL"
	for _, unicode := range []bool{false, true} {
		h := finishHarness(t, seedFinish, 120, 35, func(o *Options) { o.Color, o.Unicode = true, unicode })
		h.fin.needs[idCalendar] = SignInState{Possible: true, Needed: true}
		h.fin.needs[idMail] = SignInState{Possible: true, Needed: true}
		h.fin.url = url
		h.loads.errs[idMaps] = output.NewError("connection_failed", nil)
		h.turnOn("Calendar", "Mail", "Maps", "Search", "Wiki")
		h.fin.block = make(chan struct{})
		h.hold = true
		h.press("s")
		h.stepUntil(h.signingIn(idCalendar))
		for _, w := range []int{40, 41, 59, 80, 99, 100, 120, 160} {
			for _, ht := range []int{10, 11, 17, 24, 29, 30, 35, 50} {
				h.send(resize(w, ht))
				checkFits(t, h, w, ht, "sign-in URL")
				if ht >= 24 && !strings.Contains(strings.Join(strings.Fields(h.plain()), ""), "TAIL") {
					t.Fatalf("%dx%d: the URL's end does not show:\n%s", w, ht, h.plain())
				}
			}
		}
		h.send(resize(120, 35))
		close(h.fin.block)
		h.flush()
	}
}

// A server turned off while setting up runs is dropped from the run: one
// in line for a sign-in never gets it, and a running sign-in is canceled.
func TestSetUpDropsAServerTurnedOff(t *testing.T) {
	h := finishHarness(t, seedFinish, 120, 35)
	h.fin.needs[idCalendar] = SignInState{Possible: true, Needed: true}
	h.fin.needs[idMail] = SignInState{Possible: true, Needed: true}
	h.turnOn("Calendar", "Mail")
	h.fin.block = make(chan struct{})
	h.hold = true
	h.press("s")
	h.stepUntil(func() bool { return h.signingIn(idCalendar)() && h.m.finish.row(idMail).state == finQueued })
	mustUpdate(t, h.store, func(s *config.State) { s.Selections.Connections[idMail] = config.Selection{} })
	h.reread()
	h.press("esc") // skips Calendar: Mail is next in line, but off
	if h.m.finish != nil {
		t.Fatalf("Mail, which is off, signs in:\n%s", h.plain())
	}
	h.flush()
	if _, signIns, _ := h.fin.counts(); signIns != 1 {
		t.Fatal(signIns)
	}
	<-h.fin.ended

	// Space on the server whose sign-in runs turns it off and cancels it.
	h.hold = true
	h.press("s")
	h.stepUntil(h.signingIn(idCalendar))
	h.selectRow("Calendar")
	h.press("space")
	h.written()
	if h.m.finish != nil {
		t.Fatalf("the sign-in of a server turned off still runs:\n%s", h.plain())
	}
	h.lacks("Sign in to Calendar")
	h.flush()
	if err := <-h.fin.ended; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// written runs, from what the harness holds, the write the last key
// started, and delivers its result.
func (h *harness) written() {
	h.t.Helper()
	cmd := h.held[len(h.held)-1]
	h.held = h.held[:len(h.held)-1]
	msgs := []tea.Msg{cmd()}
	if batch, ok := msgs[0].(tea.BatchMsg); ok {
		msgs = nil
		for _, c := range batch {
			if c != nil {
				msgs = append(msgs, c())
			}
		}
	}
	for _, msg := range msgs {
		if msg, ok := msg.(writtenMsg); ok {
			h.send(msg)
			return
		}
	}
	h.t.Fatal("the last key wrote nothing")
}
