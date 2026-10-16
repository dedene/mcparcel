package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/edit"
	"github.com/dedene/mcparcel/internal/output"
)

// Every change setup makes is written at once, in its own store update,
// through the same edit functions as the matching CLI command. A change
// made elsewhere (another terminal, a sync) since setup last read the state
// is picked up first: the change is applied to the fresh state, or, when it
// no longer applies there, not made. u undoes the last change, again and
// again, back to how setup opened.

type opKind int

const (
	opEnabled opKind = iota
	opTool
	opInput
	opProfile
	opLocalAdd
	opLocalUpdate
	opLocalRemove
	opNewProfile // join: a local credential profile, created once
	opUndo       // reverts one earlier change (undoStep)
)

// op is one edit, applied through the same edit functions as the matching
// CLI command.
type op struct {
	kind    opKind
	id      string // canonical connection ID; the profile ID for opNewProfile
	name    string // tool or input name
	value   string // input value or profile name
	on      bool   // enabled, or tool enabled
	raw     []byte // personal definition bytes (add and update)
	was     []byte // update and remove: the stored definition they were made on
	profile config.Profile
	undo    *undoStep
}

// errProfileExists refuses a profile that appeared since setup read the state.
var errProfileExists = errors.New("credential profile already exists")

// errChangedElsewhere refuses an undo, a personal edit or a removal once the
// connection changed elsewhere.
var errChangedElsewhere = errors.New("changed elsewhere")

// definitionPrint is what opLocalUpdate and opLocalRemove compare.
func definitionPrint(c config.Connection) []byte {
	b, _ := json.Marshal(c)
	return b
}

func (o op) apply(s *config.State) error {
	switch o.kind {
	case opTool:
		return edit.StageTools(s, o.id, []string{o.id}, []string{o.name}, o.on)
	case opInput:
		return edit.StageInput(s, o.id, o.name, o.value)
	case opProfile:
		return edit.StageProfile(s, o.id, o.value)
	case opLocalAdd:
		_, err := edit.StageLocalAdd(s, o.raw)
		return err
	case opLocalUpdate, opLocalRemove:
		// They are made on the definition shown; one changed elsewhere since
		// is not overwritten.
		base := strings.TrimPrefix(o.id, "local:")
		if c, ok := s.Personal.Connections[base]; ok && !bytes.Equal(definitionPrint(c), o.was) {
			return errChangedElsewhere
		}
		if o.kind == opLocalRemove {
			return edit.StageLocalRemove(s, base)
		}
		return edit.StageLocalUpdate(s, base, o.raw)
	case opNewProfile:
		if _, ok := s.Local.CredentialProfiles[o.id]; ok {
			return errProfileExists
		}
		if s.Local.CredentialProfiles == nil {
			s.Local.CredentialProfiles = map[string]config.Profile{}
		}
		s.Local.CredentialProfiles[o.id] = o.profile
		return nil
	case opUndo:
		return o.undo.revert(s)
	}
	return edit.StageEnabled(s, []string{o.id}, []string{o.id}, o.on)
}

// change is one store update: its edits, in order, and the connections
// whose review it accepts, as mcparcel enable does.
type change struct {
	ops    []op
	accept []string
}

// ids lists the connections the change edits, in order, each once.
func (c change) ids() []string {
	var ids []string
	for _, o := range c.ops {
		if o.kind != opNewProfile && o.kind != opUndo && !slices.Contains(ids, o.id) {
			ids = append(ids, o.id)
		}
	}
	return ids
}

// writeResult is what writing a change found: the state it wrote or, after
// an error, a fresh read (fresh), so setup shows what is stored either way.
type writeResult struct {
	state  config.State
	before config.State // the state the change was applied to
	fresh  bool
	moved  bool // the state had changed elsewhere since setup last read it
	err    error
}

// writeChange writes c on top of revision. When the store has moved on, it
// applies c once more, to the fresh state.
func writeChange(ctx context.Context, store *config.Store, revision uint64, c change) writeResult {
	var r writeResult
	for {
		next, err := store.UpdateAccepted(ctx, revision, c.accept, func(s *config.State) error {
			before, err := config.CloneState(*s)
			if err != nil {
				return err
			}
			r.before = before
			for _, o := range c.ops {
				if err := o.apply(s); err != nil {
					return err
				}
			}
			return nil
		})
		if err == nil {
			r.state, r.fresh = next, true
			return r
		}
		fresh, readErr := store.Read(ctx)
		if errors.Is(err, config.ErrConfigConflict) && !r.moved && readErr == nil && fresh.Selections.Revision != revision {
			r.moved, revision = true, fresh.Selections.Revision
			continue
		}
		r.state, r.fresh, r.err = fresh, readErr == nil, err
		return r
	}
}

// live is the state setup last read or wrote, and the changes u can undo.
type live struct {
	state     config.State
	effective config.EffectiveConfig
	undo      []*undoStep
}

func newLive(state config.State) (*live, error) {
	l := &live{}
	return l, l.set(state)
}

func (l *live) State() config.State               { return l.state }
func (l *live) Effective() config.EffectiveConfig { return l.effective }

// set shows a state setup read or wrote.
func (l *live) set(state config.State) error {
	effective, err := config.Resolve(state)
	if err != nil {
		return err
	}
	l.state, l.effective = state, effective
	return nil
}

// trial applies c to a copy of the state shown, without writing it.
func (l *live) trial(c change) error {
	next, err := config.CloneState(l.state)
	if err != nil {
		return err
	}
	for _, o := range c.ops {
		if err := o.apply(&next); err != nil {
			return err
		}
	}
	_, err = config.Resolve(next)
	return err
}

// undoStep reverts one change: each connection it edited gets back the
// selection and, for a personal one, the definition it had before, as long
// as nothing else changed them since.
type undoStep struct {
	ids           []string
	before, after config.State
	done          string // the status once undone
}

// record keeps a written change for u. A change that wrote nothing, or
// edited no connection, is not kept.
func (l *live) record(c change, before, after config.State) {
	ids := c.ids()
	if len(ids) == 0 || before.Selections.Revision == after.Selections.Revision {
		return
	}
	was, err := config.Resolve(before)
	if err != nil {
		return
	}
	l.undo = append(l.undo, &undoStep{ids: ids, before: before, after: after, done: undoneText(c, was, l.effective)})
}

// last is the change u undoes next, or nil.
func (l *live) last() *undoStep {
	if len(l.undo) == 0 {
		return nil
	}
	return l.undo[len(l.undo)-1]
}

// drop forgets an undo step: undone, or one that can no longer be.
func (l *live) drop(u *undoStep) {
	l.undo = slices.DeleteFunc(l.undo, func(v *undoStep) bool { return v == u })
}

// accept lists the connections the undo turns back on as they were: on,
// ready and accepted. Restoring a value of one that is on would otherwise
// ask for a review of what it already ran.
func (u *undoStep) accept() []string {
	was, err := config.Resolve(u.before)
	if err != nil {
		return nil
	}
	var ids []string
	for _, id := range u.ids {
		row := was.Connections[id]
		if sel := u.before.Selections.Connections[id]; sel.Enabled && !sel.ReviewRequired && row.Available && !slices.Contains(row.Blockers, "config_required") {
			ids = append(ids, id)
		}
	}
	return ids
}

// connectionPrint is what the undo step guards for id: its selection and
// its definition.
func connectionPrint(s config.State, effective config.EffectiveConfig, id string) string {
	var p struct {
		Selection  *config.Selection  `json:"s,omitempty"`
		Definition *config.Connection `json:"d,omitempty"`
	}
	if sel, ok := s.Selections.Connections[id]; ok {
		p.Selection = &sel
	}
	if row, ok := effective.Connections[id]; ok {
		p.Definition = row.Definition
	}
	b, _ := json.Marshal(p)
	return string(b)
}

func (u *undoStep) revert(s *config.State) error {
	now, err := config.Resolve(*s)
	if err != nil {
		return err
	}
	then, err := config.Resolve(u.after)
	if err != nil {
		return err
	}
	for _, id := range u.ids {
		if connectionPrint(*s, now, id) != connectionPrint(u.after, then, id) {
			return errChangedElsewhere
		}
	}
	for _, id := range u.ids {
		if sel, ok := u.before.Selections.Connections[id]; ok {
			sel.Inputs, sel.DisabledTools = maps.Clone(sel.Inputs), slices.Clone(sel.DisabledTools)
			s.Selections.Connections[id] = sel
		} else {
			delete(s.Selections.Connections, id)
		}
		if base, ok := strings.CutPrefix(id, "local:"); ok {
			if c, ok := u.before.Personal.Connections[base]; ok {
				s.Personal.Connections[base] = c
			} else {
				delete(s.Personal.Connections, base)
			}
		}
	}
	// Declarations a personal definition brought in go with it, unless
	// another personal connection uses them.
	for name := range u.after.Personal.Domains {
		if _, ok := u.before.Personal.Domains[name]; !ok && !personalUses(s.Personal, func(c config.Connection) bool { return slices.Contains(c.Domains, name) }) {
			delete(s.Personal.Domains, name)
		}
	}
	for name := range u.after.Personal.CredentialProfiles {
		if _, ok := u.before.Personal.CredentialProfiles[name]; !ok && !personalUses(s.Personal, func(c config.Connection) bool { return c.CredentialProfile == name }) {
			delete(s.Personal.CredentialProfiles, name)
		}
	}
	return nil
}

func personalUses(p config.Catalog, uses func(config.Connection) bool) bool {
	for _, c := range p.Connections {
		if uses(c) {
			return true
		}
	}
	return false
}

// undoneText is the status once c is undone, from what c was applied to
// (was) and what it wrote (now). Labels and names only, never values.
func undoneText(c change, was, now config.EffectiveConfig) string {
	label := func(id string) string {
		if row, ok := now.Connections[id]; ok {
			return rowLabel(row)
		}
		return output.DisplayMetadata(id)
	}
	enables := 0
	for _, o := range c.ops {
		if o.kind == opEnabled && o.on {
			enables++
		}
	}
	if enables > 1 {
		return plural(enables, "server is", "servers are") + " off again."
	}
	o := c.ops[len(c.ops)-1]
	row, known := was.Connections[o.id]
	switch o.kind {
	case opEnabled:
		switch {
		case !o.on:
			return label(o.id) + " is on again."
		case known && row.Enabled && row.ReviewRequired:
			return label(o.id) + " needs a review again."
		}
		return label(o.id) + " is off again."
	case opTool:
		state := "off"
		if !o.on {
			state = "on"
		}
		return "Tool " + output.DisplayMetadata(o.name) + " of " + label(o.id) + " is " + state + " again."
	case opInput:
		return "Input " + output.DisplayMetadata(o.name) + " of " + label(o.id) + " is back as it was."
	case opProfile:
		return "The 1Password link of " + label(o.id) + " is back as it was."
	case opLocalAdd:
		return label(o.id) + " is removed again."
	case opLocalRemove:
		return rowLabel(row) + " is back."
	}
	return label(o.id) + " is back as it was."
}
