package ui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// A later run that is stopped before a connection settles leaves it no
// outcome: what an earlier run found no longer holds.
func TestFinishStoppedRunDropsOldOutcome(t *testing.T) {
	h := finishHarness(t, seedFinish, 120, 35)
	h.turnOn("Maps")
	h.press("enter", "enter") // start, then done
	if r := h.m.Result(); !slices.Equal(r.Started, []string{idMaps}) {
		t.Fatal(r)
	}
	h.press("space", "ctrl+s") // Maps off
	if h.m.screen == screenFinish {
		h.press("esc")
	}
	h.press("space", "ctrl+s") // on again: offered
	if h.m.screen != screenFinish {
		t.Fatalf("no offer:\n%s", h.plain())
	}
	h.hold = true
	h.press("enter", "esc")
	h.flush()
	if r := h.m.Result(); r.Started != nil || r.SignIn != nil || r.Failed != nil {
		t.Fatal("a stopped connection kept its earlier outcome:", r)
	}
}

// After the finish step the list and details say what it found: why a
// connection could not start, with its next step on the tools screen, that
// one started, or that one still needs a sign-in.
func TestFinishOutcomeShowsInList(t *testing.T) {
	h := finishHarness(t, seedFinish, 120, 35)
	failure := output.NewError("connection_failed", nil)
	h.loads.errs[idMaps] = failure
	h.fin.needs[idMail] = SignInState{Possible: true, Needed: true}
	h.turnOn("Maps", "Wiki", "Mail")
	h.press("enter", "s", "enter")
	if h.m.screen != screenList {
		t.Fatal(h.m.screen)
	}
	h.selectRow("Maps")
	h.paneContains("Not started. " + failure.Message)
	h.selectRow("Wiki")
	h.paneContains("Started this session, 2 tools.")
	h.selectRow("Mail")
	h.paneContains("Needs a sign-in: run mcparcel auth local:mail.")
	h.lacks("Connection not checked.")
	h.send(resize(80, 24)) // the compact list's state line
	h.selectRow("Maps")
	h.containsWrapped("Not started. " + failure.Message)
	h.send(resize(120, 35))
	h.details("Other", "Maps")
	h.containsWrapped("Not started. " + failure.Message)
	h.press("t")
	if failure.NextAction != "" {
		h.containsWrapped(failure.NextAction)
	}
}

// A sign-in URL longer than the screen is never out of reach: Up/Down
// scroll the whole page, URL included, down to the rows.
func TestFinishLongURLScrolls(t *testing.T) {
	url := "https://auth.example/authorize?response_type=code&client_id=" + strings.Repeat("c", 40) + "&redirect_uri=http%3A%2F%2F127.0.0.1%3A49152%2Fcallback&code_challenge=" + strings.Repeat("x", 43) + "&code_challenge_method=S256&state=" + strings.Repeat("s", 43) + "&scope=openid%20profile%20offline_access&resource=https%3A%2F%2Fmcp.example%2Fmcp&end=TAIL"
	for _, size := range [][2]int{{80, 12}, {40, 12}, {40, 10}} {
		h := finishHarness(t, seedFinish, 120, 35)
		h.fin.needs[idCalendar] = SignInState{Possible: true, Needed: true}
		h.fin.url = url
		h.turnOn("Calendar", "Wiki")
		h.press("enter")
		h.fin.block = make(chan struct{})
		h.hold = true
		h.press("enter")
		msg := firstMsg(h.held[0])
		h.held = nil
		h.send(msg)
		h.send(resize(size[0], size[1]))
		if size[0] == 40 {
			h.contains("Up/Down scroll   s skip   Esc stop")
		}
		var seen strings.Builder
		for range 30 {
			seen.WriteString(h.plain())
			h.press("down")
		}
		all := seen.String()
		if !strings.Contains(all, "TAIL") || !strings.Contains(all, "waiting for browser") || !strings.Contains(all, "Wiki") {
			t.Fatalf("%v: the URL's end or the rows never showed:\n%s", size, h.plain())
		}
		close(h.fin.block)
		h.flush()
	}
}

// A long label keeps the room the line has; a connection waiting for a
// free slot does not spin.
func TestFinishLongLabelAndWaitingMark(t *testing.T) {
	h := finishHarness(t, seedFinish, 120, 35, func(o *Options) { o.Color, o.Unicode = true, true })
	long := "Z personal server with a really quite long label" // sorts last: it waits
	mustUpdate(t, h.store, func(s *config.State) {
		c := s.Personal.Connections["wiki"]
		c.Label = long
		s.Personal.Connections["wiki"] = c
	})
	h.run(h.m.reload())
	h.turnOn("Maps", "Search", "Calendar", "Mail", long)
	h.hold = true
	h.press("enter")
	h.lineHas(long, "waiting")
	frames := h.m.theme.spinner.Frames
	if line := h.rowLine(long); !strings.Contains(line, frames[0]+" "+long) {
		t.Fatalf("%q", line)
	}
	h.hold = false
	h.ticks = nil
	batch := h.held[0]().(tea.BatchMsg)
	h.held = nil
	h.run(batch[len(batch)-1])
	h.fireTick()
	if line := h.rowLine(long); !strings.Contains(line, frames[0]+" "+long) {
		t.Fatalf("a waiting row spins: %q", line)
	}
}
