package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// A status message is a toast: it clears after toastLife, dimming first when
// motion is on. Ticks wait for the test, so this is deterministic.
func TestToastClearsAfterTicks(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35, fixedNow())
	h.press("ctrl+s")
	h.contains("Nothing to save.")
	if len(h.ticks) != 1 || h.ticks[0].d != toastLife {
		t.Fatal(h.ticks)
	}
	h.fireTick() // no color, no motion: it clears at once
	h.lacks("Nothing to save.")
	if len(h.ticks) != 0 {
		t.Fatal(h.ticks)
	}

	color := newHarness(t, seedFixture, 120, 35, fixedNow(), func(o *Options) { o.Color = true })
	color.press("ctrl+s")
	color.fireTick()
	if !painted(color.view(), "Nothing to save.", "90") || len(color.ticks) != 1 || color.ticks[0].d != toastFade {
		t.Fatalf("not fading: %q %v", color.view(), color.ticks)
	}
	color.fireTick()
	color.lacks("Nothing to save.")

	// A newer status restarts the clock: the older tick does nothing.
	h.press("ctrl+s")
	h.selectRow("Figma")
	h.press("space", "ctrl+s", "esc") // Esc: the finish step later
	h.contains("Saved at revision")
	if len(h.ticks) != 2 {
		t.Fatal(h.ticks)
	}
	h.fireTick()
	h.contains("Saved at revision")
	h.fireTick()
	h.lacks("Saved at revision")

	// A busy status stays until its result replaces it.
	h.press("space")
	h.hold = true
	h.press("ctrl+s")
	h.contains("Saving...")
	if len(h.ticks) != 0 {
		t.Fatal(h.ticks)
	}
	h.flush()
	if len(h.ticks) != 1 {
		t.Fatal(h.ticks)
	}
}

// The prompt of a pending enable is guidance, not a toast: it stays until
// the enable ends, and the status that ends it is a toast again.
func TestPendingEnablePromptStays(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35, fixedNow())
	h.selectTab("Research")
	h.selectRow("Notes")
	h.press("space")
	h.contains("Notes needs: input workspace, credential profile. Esc keeps it off.")
	if len(h.ticks) != 0 {
		t.Fatal(h.ticks)
	}
	h.press("esc")
	h.contains("Notes stays off.")
	if len(h.ticks) != 1 {
		t.Fatal(h.ticks)
	}
}

// Toggling a row flashes it for flashLife, then it settles to the focus
// style. Off and on flash differently.
func TestToggleFlashesRow(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35, fixedNow(), func(o *Options) { o.Color = true })
	h.selectRow("Figma")
	h.press("space")
	if h.m.flashID != idFigma || len(h.ticks) != 1 || h.ticks[0].d != flashLife || !painted(h.view(), "> [x] Figma", "32") {
		t.Fatalf("%v %v %q", h.m.flashID, h.ticks, h.view())
	}
	h.fireTick()
	if h.m.flashID != "" || painted(h.view(), "> [x] Figma", "32") || !painted(h.view(), "> [x] Figma", "7") {
		t.Fatalf("did not settle: %q", h.view())
	}
	h.press("space")
	if !painted(h.view(), "> [ ] Figma", "90") {
		t.Fatalf("%q", h.view())
	}
	// A failed toggle (an unavailable row that is off) does not flash.
	h.fireTick()
	h.selectTab("Other")
	h.selectRow("local:gone")
	h.press("space", "space")
	h.fireTick()
	h.press("space")
	if h.m.flashID != "" {
		t.Fatal(h.m.flashID)
	}
}

// The loading state spins with motion, one frame per tick, and stops when
// the load ends.
func TestSpinnerWhileLoading(t *testing.T) {
	h := toolHarness(t, seedFixture, func(o *Options) { o.Color, o.Unicode = true, true })
	h.details("Other", "Excalidraw")
	h.hold = true
	h.press("l")
	h.hold = false
	batch := h.held[0]().(tea.BatchMsg) // the load, then the spinner
	h.held = nil
	h.run(batch[1])
	frames := h.m.theme.spinner.Frames
	h.contains(frames[0] + " Connecting to Excalidraw... Credentials may be requested.")
	if len(h.ticks) != 1 || h.ticks[0].d != frameRate {
		t.Fatal(h.ticks)
	}
	h.fireTick()
	h.contains(frames[1] + " Connecting to Excalidraw...")
	h.run(batch[0])
	h.contains("Loaded this session")
	h.fireTick() // the load ended: no next frame
	if len(h.ticks) != 0 {
		t.Fatal(h.ticks)
	}
}

// With reduced motion nothing animates: no flash, a static "Loading...",
// and the only tick is the one that clears a toast, which clears at once.
func TestReduceMotionSchedulesNoAnimation(t *testing.T) {
	h := toolHarness(t, seedFixture, func(o *Options) { o.Color, o.Unicode, o.ReduceMotion = true, true, true })
	h.selectRow("Figma")
	h.press("space", "space", "down", "space")
	if len(h.ticks) != 0 || h.m.flashID != "" {
		t.Fatal(h.ticks, h.m.flashID)
	}
	h.press("ctrl+s", "esc")
	h.details("Other", "Excalidraw")
	h.ticks = nil
	h.hold = true
	h.press("l")
	h.contains("Loading... Connecting to Excalidraw...")
	for _, frame := range h.m.theme.spinner.Frames {
		if strings.Contains(h.plain(), frame+" Connecting") {
			t.Fatal(h.plain())
		}
	}
	h.flush()
	h.contains("Loaded this session")
	if len(h.ticks) != 0 {
		t.Fatal(h.ticks)
	}
	h.press("esc", "esc", "ctrl+s")
	h.contains("Nothing to save.")
	if len(h.ticks) != 1 || h.ticks[0].d != toastLife {
		t.Fatal(h.ticks)
	}
	h.fireTick()
	h.lacks("Nothing to save.")
	if len(h.ticks) != 0 {
		t.Fatal(h.ticks)
	}
}

// Nothing is scheduled while idle: not by Init, a resize or browsing.
func TestIdleSchedulesNothing(t *testing.T) {
	h := newHarness(t, seedFixture, 120, 35, func(o *Options) { o.Color, o.Unicode = true, true })
	if h.m.Init() != nil {
		t.Fatal("Init schedules a command")
	}
	h.send(resize(100, 30))
	h.press("right", "down", "up", "left", "?", "esc", "enter", "esc")
	if len(h.ticks) != 0 {
		t.Fatal(h.ticks)
	}
}
