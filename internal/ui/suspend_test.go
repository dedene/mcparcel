package ui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/config"
)

// Ctrl+Z suspends from every screen and is never typed into the text the
// screen edits.
func TestCtrlZSuspendsFromEveryScreen(t *testing.T) {
	cases := []struct {
		name string
		open func(*harness)
		text func(*harness) string
		want screen
	}{
		{"list", func(*harness) {}, nil, screenList},
		{"search", func(h *harness) { h.press("/"); h.typeText("fig") }, func(h *harness) string { return h.m.search.String() }, screenSearch},
		{"help", func(h *harness) { h.press("?") }, nil, screenHelp},
		{"server page", func(h *harness) { h.details("Design", "Figma") }, nil, screenDetails},
		{"input on the server page", func(h *harness) {
			h.details("Research", "Notes")
			h.do("Set workspace")
			h.typeText("ws")
		}, func(h *harness) string { return h.m.input.value.String() }, screenDetails},
		{"field", func(h *harness) {
			h.selectTab("Research")
			h.selectRow("Notes")
			h.press("space")
			h.typeText("ws")
		}, func(h *harness) string { return h.m.input.value.String() }, screenField},
		{"tools", func(h *harness) { h.details("Other", "Excalidraw"); h.do("Choose tools") }, nil, screenTools},
		{"form", func(h *harness) { h.press("n"); h.typeText("Mine") }, func(h *harness) string { return h.m.form.current().value.String() }, screenForm},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := toolHarness(t, seedFixture)
			c.open(h)
			if h.m.screen != c.want {
				t.Fatalf("opened %v, want %v:\n%s", h.m.screen, c.want, h.plain())
			}
			before, writes := "", h.writes()
			if c.text != nil {
				before = c.text(h)
			}
			h.press("ctrl+z")
			if h.suspends != 1 || h.m.screen != c.want || h.writes() != writes {
				t.Fatal(h.suspends, h.m.screen, h.writes())
			}
			if c.text != nil && c.text(h) != before {
				t.Fatalf("Ctrl+Z typed into the text: %q, was %q", c.text(h), before)
			}
		})
	}

	join := newHarness(t, seedJoin, 120, 35, withJoin)
	join.press("ctrl+z")
	join.send(tea.ResumeMsg{})
	join.press("e")
	join.typeText("me")
	join.press("ctrl+z")
	if join.suspends != 2 || join.m.screen != screenJoin || join.m.join.account.String() != "me" {
		t.Fatal(join.suspends, join.m.screen, join.m.join.account.String())
	}

	small := newHarness(t, seedFixture, 39, 10)
	small.press("ctrl+z")
	if small.suspends != 1 {
		t.Fatal("Ctrl+Z does not suspend on the too-small screen")
	}
}

// Ctrl+Z pressed while a change is written waits for it, as every key
// does: setup never stops with a write half way.
func TestCtrlZDuringWriteWaitsForIt(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.selectRow("Figma")
	h.hold = true
	h.press("space", "ctrl+z")
	if h.suspends != 0 || !h.m.busy {
		t.Fatal(h.suspends, h.m.busy)
	}
	h.flush()
	if h.suspends != 1 || !h.disk().Selections.Connections[idFigma].Enabled {
		t.Fatal(h.suspends, h.disk().Selections.Connections[idFigma])
	}
}

// Keys pressed after a Ctrl+Z that waited for a write wait in turn until
// setup resumes: one that starts a write never runs while setup suspends.
func TestQueuedCtrlZHoldsLaterKeysUntilResume(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	h.selectRow("Figma")
	h.press("down")
	below, _ := h.m.selected()
	h.press("up")
	was := h.disk().Selections.Connections[below.ID].Enabled

	h.hold = true
	h.press("space", "ctrl+z", "down", "space")
	if len(h.held) != 1 || len(h.m.queued) != 3 {
		t.Fatal(len(h.held), len(h.m.queued))
	}
	// Deliver the write by hand, to see what the one Update it ends returns.
	h.hold = false
	write := h.held[0]
	h.held = nil
	_, cmd := h.m.Update(write())
	if h.m.busy || !h.m.suspending {
		t.Fatal("a key queued after Ctrl+Z started a write alongside the suspend", h.m.busy, h.m.suspending)
	}
	h.run(cmd)
	if h.suspends != 1 || len(h.m.queued) != 2 {
		t.Fatal(h.suspends, len(h.m.queued))
	}
	disk := h.disk().Selections.Connections
	if !disk[idFigma].Enabled || disk[below.ID].Enabled != was {
		t.Fatal("the writes did not wait for the resume", disk[idFigma], disk[below.ID])
	}

	h.send(tea.ResumeMsg{})
	if h.m.suspending || len(h.m.queued) != 0 || h.disk().Selections.Connections[below.ID].Enabled == was {
		t.Fatal("the keys that waited did not run on resume", h.m.suspending, len(h.m.queued))
	}
}

// Back from a suspend, setup shows what changed elsewhere meanwhile, reads
// the stored sign-ins and the commands again, and clears the screen.
func TestResumeReadsAgain(t *testing.T) {
	installed := false
	h := statusHarness(t, func(o *Options) {
		o.CommandAvailable = func(command, _ string) bool { return command != "maps-fixture" || installed }
	})
	h.selectTab("Other")
	h.lineHas("Calendar", "sign in")
	h.lineHas("Maps", "not installed")
	checks, _, _ := h.fin.counts()

	h.press("ctrl+z")
	installed = true
	h.fin.mu.Lock()
	h.fin.needs[idCalendar] = SignInState{Possible: true}
	h.fin.mu.Unlock()
	mustUpdate(t, h.store, func(s *config.State) {
		sel := s.Selections.Connections[idWiki]
		sel.Enabled = !sel.Enabled
		s.Selections.Connections[idWiki] = sel
	})
	wiki := h.disk().Selections.Connections[idWiki].Enabled
	h.send(tea.ResumeMsg{})

	if h.m.live.State().Selections.Connections[idWiki].Enabled != wiki || h.m.live.State().Selections.Revision != h.disk().Selections.Revision {
		t.Fatal("the change made while suspended does not show")
	}
	if again, _, _ := h.fin.counts(); again <= checks {
		t.Fatal("the stored sign-ins were not read again", checks, again)
	}
	h.lineHas("Calendar", "starts on first use")
	h.lineHas("Maps", "starts on first use")

	cmd := h.m.resumed()
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || !clears(batch) {
		t.Fatal("resume does not clear the screen")
	}
}

func clears(batch tea.BatchMsg) bool {
	for _, c := range batch {
		if c != nil && fmt.Sprintf("%T", c()) == fmt.Sprintf("%T", tea.ClearScreen()) {
			return true
		}
	}
	return false
}

// A server whose page or edit form shows, removed elsewhere while setup
// was suspended, returns to the list, from help over its page too.
func TestResumeLeavesAPageWhoseServerIsGone(t *testing.T) {
	for _, c := range []struct {
		help, edit bool
	}{{false, false}, {true, false}, {false, true}} { // ? types into the form
		help := c.help
		h := newHarness(t, seedFixture, 120, 35)
		h.details("Other", "Excalidraw")
		if c.edit {
			h.do("Edit")
			if h.m.screen != screenForm {
				t.Fatal(h.m.screen)
			}
		}
		if help {
			h.press("?")
		}
		h.press("ctrl+z")
		mustUpdate(t, h.store, func(s *config.State) {
			delete(s.Personal.Connections, "excalidraw")
			delete(s.Selections.Connections, idExcali)
		})
		h.send(tea.ResumeMsg{})
		if help {
			if h.m.screen != screenHelp || h.m.back != screenList {
				t.Fatal(h.m.screen, h.m.back)
			}
			h.press("esc")
		}
		if h.m.screen != screenList || h.m.form != nil {
			t.Fatal(h.m.screen, h.m.form)
		}
		h.contains("Excalidraw is gone: it changed elsewhere.")
	}

	// A form adding a new connection stays, whatever else is gone.
	h := newHarness(t, seedFixture, 120, 35)
	h.details("Other", "Excalidraw")
	h.press("esc", "n")
	h.typeText("Mine")
	h.press("ctrl+z")
	mustUpdate(t, h.store, func(s *config.State) {
		delete(s.Personal.Connections, "excalidraw")
		delete(s.Selections.Connections, idExcali)
	})
	h.send(tea.ResumeMsg{})
	if h.m.screen != screenForm || h.m.form == nil || h.m.form.current().value.String() != "Mine" {
		t.Fatal("a new-connection form closed on resume", h.m.screen)
	}
}

// A sign-in read that ran before the suspend is dropped on resume: setup
// reads again, since a sign-in in the shell meanwhile may have settled it.
// The old read loses also when it returns after the resume started the
// new one, and the new read's answer is kept.
func TestResumeDropsASignInReadFromBeforeSuspend(t *testing.T) {
	for _, late := range []bool{false, true} {
		h := statusHarness(t)
		h.selectTab("Other")
		clear(h.m.signIns)
		stale := h.m.readSignIns()()
		h.press("ctrl+z")
		h.fin.mu.Lock()
		h.fin.needs[idCalendar] = SignInState{Possible: true}
		h.fin.mu.Unlock()
		h.hold = late
		h.send(tea.ResumeMsg{})
		h.hold = false
		held := h.held
		h.held = nil
		h.send(stale)
		for _, cmd := range held {
			h.run(cmd)
		}
		h.lineHas("Calendar", "starts on first use")
	}
}

// A read that fails on resume says so, without any secret value, and
// keeps the state shown; one that fails while a change is written is
// dropped, as any read then is.
func TestResumeReadFailureKeepsTheStateShown(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	before := h.m.live.State()
	h.send(reloadedMsg{err: errors.New("read op://Private/item/secret failed")})
	if h.m.live.State().Selections.Revision != before.Selections.Revision {
		t.Fatal("a failed read replaced the state shown")
	}
	if !strings.HasPrefix(h.m.status, "Could not read the configuration again: ") || strings.Contains(h.m.status, "op://") || strings.Contains(h.m.status, "secret") {
		t.Fatal(h.m.status)
	}
	h.contains("Could not read the configuration again")
	h.m.status = ""
	h.m.busy = true
	h.send(reloadedMsg{err: errors.New("boom")})
	if h.m.status != "" {
		t.Fatal("a failed read was shown while a change was written:", h.m.status)
	}
}

// A read older than what setup shows, or one that returns while a change
// is written, is dropped: the write brings a fresher state.
func TestResumeDropsAnOlderRead(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	old := h.disk()
	h.selectRow("Figma")
	h.press("space")
	h.send(reloadedMsg{state: old})
	if !h.m.live.State().Selections.Connections[idFigma].Enabled {
		t.Fatal("an older read replaced the state shown")
	}
	h.m.busy = true
	fresh := mustUpdate(t, h.store, func(s *config.State) { s.Local.Aliases = map[string]string{"p": idPaper} })
	h.send(reloadedMsg{state: fresh})
	if h.m.live.State().Selections.Revision == fresh.Selections.Revision {
		t.Fatal("a read was shown while a change was written")
	}
}

// Without job control Ctrl+Z does not suspend; it says so.
func TestCtrlZWithoutJobControlSaysSo(t *testing.T) {
	was := canSuspend
	canSuspend = false
	t.Cleanup(func() { canSuspend = was })
	h := newHarness(t, seedFixture, 120, 35)
	h.press("ctrl+z")
	if h.suspends != 0 {
		t.Fatal(h.suspends)
	}
	h.contains("Ctrl+Z cannot suspend setup here; q quits.")
}

// Keys read after a Ctrl+Z, before Bubble Tea stops the process, wait for
// the resume: one of them may start a write, which must not run while setup
// is suspended.
func TestKeysAfterDirectCtrlZWaitForResume(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35)
	writes := h.writes()
	h.press("ctrl+z", "space")
	if h.writes() != writes || h.m.busy || !h.m.suspending || len(h.m.queued) != 1 {
		t.Fatal("a key after Ctrl+Z ran before the resume", h.writes(), h.m.busy, h.m.suspending, len(h.m.queued))
	}
	h.send(tea.ResumeMsg{})
	if h.m.suspending || len(h.m.queued) != 0 || h.writes() == writes {
		t.Fatal("the waiting key did not run on resume", h.writes(), len(h.m.queued))
	}
}
