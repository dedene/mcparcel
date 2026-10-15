package ui

import (
	"path"
	"slices"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/edit"
	"github.com/dedene/mcparcel/internal/output"
)

// A row's look is derived from the draft state and the definition's metadata
// only: never a credential, never the runtime. Every view (wide list, compact
// list, details pane, details page, help legend) reads it through lookOf, so
// they agree.

// rowState is what a row's glyph says.
type rowState int

const (
	rowOff         rowState = iota // off; Space turns it on (a profile Space links by itself counts as ready)
	rowOn                          // on and ready
	rowNeedsConfig                 // an input, or a profile Space cannot link by itself, is missing
	rowReview                      // on, changed upstream: review required
	rowUnavailable                 // its source no longer defines it
)

type needKind int

const (
	needStatus  needKind = iota // review or unavailable, list only
	needInput                   // a declared input
	needProfile                 // a credential profile (1Password)
	needSignIn                  // an OAuth sign-in in a browser
	needCommand                 // a local command the row runs
)

// need is one thing a row needs to run.
type need struct {
	kind needKind
	text string // the list's short form
	name string // the checklist's name, when it differs from text
	note string // the checklist's note
	role role
	done bool // nothing is left to do for it
}

// rowLook is a row's visual state and what it needs.
type rowLook struct {
	state rowState
	on    bool
	needs []need
}

func lookOf(state config.State, row config.EffectiveConnection) rowLook {
	l := rowLook{on: row.Enabled}
	switch {
	case !row.Available:
		l.state = rowUnavailable
		l.needs = append(l.needs, need{kind: needStatus, text: "unavailable", role: roleBad})
	case row.Enabled && row.ReviewRequired:
		l.state = rowReview
		l.needs = append(l.needs, need{kind: needStatus, text: "review", role: roleWarn})
	case slices.Contains(row.Blockers, "config_required") && autoLinks(state, row) == "":
		l.state = rowNeedsConfig
	case row.Enabled:
		l.state = rowOn
	}
	l.needs = append(l.needs, needsOf(state, row)...)
	return l
}

// needsOf lists what the definition asks for: each input (missing ones
// first), the credential profile, an OAuth sign-in and the local command.
func needsOf(state config.State, row config.EffectiveConnection) []need {
	d := row.Definition
	if d == nil {
		return nil
	}
	missing, profileMissing := config.MissingConfig(state, row)
	var set, unset []need
	for _, name := range sortedInputs(d) {
		n := need{kind: needInput, text: "input " + output.DisplayMetadata(name), role: roleWarn}
		if slices.Contains(missing, name) {
			n.note = "not set"
			unset = append(unset, n)
		} else {
			n.note, n.role, n.done = "set", roleDim, true
			set = append(set, n)
		}
	}
	out := append(unset, set...)
	if req := d.CredentialProfile; req != "" {
		n := need{kind: needProfile, text: "1Password", name: "1Password " + output.DisplayMetadata(req), role: roleInfo, done: true}
		bound := state.Selections.Connections[row.ID].CredentialProfile
		switch {
		case !profileMissing && bound == req:
			n.note = "linked"
		case !profileMissing:
			n.note = "linked to " + output.DisplayMetadata(bound)
		case edit.AutoProfile(state, row) != "" && enabling(row):
			n.note = "links automatically"
		default:
			n.note, n.role, n.done = "not linked", roleWarn, false
		}
		out = append(out, n)
	}
	if signIn(d) {
		out = append(out, need{kind: needSignIn, text: "sign-in", note: "in a browser on first connect", role: roleAccent})
	}
	if s := d.Transport.Stdio; s != nil {
		command := "local command"
		if s.Command.Literal != nil {
			command = output.DisplayMetadata(path.Base(*s.Command.Literal))
		}
		out = append(out, need{kind: needCommand, text: command, note: "runs on this device", role: roleDim})
	}
	return out
}

// signIn reports whether the connection signs in through a browser: HTTP
// with OAuth, except the client_credentials grant, which needs no user.
func signIn(d *config.Connection) bool {
	return d.Transport.HTTP != nil && d.Auth != nil && d.Auth.Grant != config.GrantClientCredentials
}

// ready reports whether the row is on with nothing left to do.
func (l rowLook) ready() bool { return l.state == rowOn }

// mark is the row's glyph and the role that colors it. An unavailable row
// that is off shows the off glyph; its Needs text says unavailable.
func (l rowLook) mark(g glyphs) (string, role) {
	switch {
	case l.state == rowUnavailable && l.on:
		return g.unavailable, roleBad
	case l.state == rowReview:
		return g.review, roleWarn
	case l.state == rowNeedsConfig && l.on:
		return g.pending, roleWarn
	case l.state == rowNeedsConfig:
		return g.needsConfig, roleDim
	case l.state == rowOn:
		return g.on, roleOK
	}
	return g.off, roleDim
}

// label is the long state text of the details page and pane.
func (l rowLook) label() string {
	onOff := "Off"
	if l.on {
		onOff = "On"
	}
	switch l.state {
	case rowNeedsConfig:
		return onOff + ", configuration required"
	case rowReview:
		return onOff + ", review required"
	case rowUnavailable:
		return onOff + ", unavailable"
	}
	return onOff
}

// listNeeds is the list's Needs column: the status, then each need still
// open, then the done ones (a linked profile), so a narrow column shows what
// is left first; a set input is left out.
func (l rowLook) listNeeds() []need {
	var open, done []need
	for _, n := range l.needs {
		switch {
		case !n.done:
			open = append(open, n)
		case n.kind != needInput:
			done = append(done, n)
		}
	}
	return append(open, done...)
}

// checklist is the details pane's Needs: everything but the status.
func (l rowLook) checklist() []need {
	var out []need
	for _, n := range l.needs {
		if n.kind != needStatus {
			out = append(out, n)
		}
	}
	return out
}
