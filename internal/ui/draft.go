package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/edit"
	"github.com/dedene/mcparcel/internal/output"
)

type opKind int

const (
	opEnabled opKind = iota
	opTool
	opInput
	opProfile
	opLocalAdd
	opLocalUpdate
)

// op is one change in the draft journal, replayed through the same edit
// functions as the matching CLI command.
type op struct {
	kind  opKind
	id    string // canonical connection ID
	name  string // tool or input name
	value string // input value or profile name
	on    bool   // enabled, or tool enabled
	raw   []byte // personal definition bytes (add and update)
}

func (o op) key() string {
	switch o.kind {
	case opTool:
		return "tool\x00" + o.id + "\x00" + o.name
	case opInput:
		return "input\x00" + o.id + "\x00" + o.name
	case opProfile:
		return "profile\x00" + o.id
	case opLocalAdd, opLocalUpdate:
		return "local\x00" + o.id
	}
	return "enabled\x00" + o.id
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
	case opLocalUpdate:
		return edit.StageLocalUpdate(s, strings.TrimPrefix(o.id, "local:"), o.raw)
	}
	return edit.StageEnabled(s, []string{o.id}, []string{o.id}, o.on)
}

// describe names the change for status lines: labels only, never values.
func (o op) describe(effective config.EffectiveConfig) string {
	label := output.DisplayMetadata(o.id)
	if row, ok := effective.Connections[o.id]; ok {
		label = rowLabel(row)
	}
	verb := "disable"
	if o.on {
		verb = "enable"
	}
	switch o.kind {
	case opTool:
		return verb + " tool " + output.DisplayMetadata(o.name) + " on " + label
	case opInput:
		return "set input " + output.DisplayMetadata(o.name) + " on " + label
	case opProfile:
		return "bind a credential profile to " + label
	case opLocalAdd:
		return "add personal connection " + label
	case opLocalUpdate:
		return "edit personal connection " + label
	}
	return verb + " " + label
}

// fingerprint is the value of one journal field in s. The enabled field
// includes ReviewRequired, so accepting a review counts as a change.
func fingerprint(s config.State, key string) string {
	parts := strings.Split(key, "\x00")
	sel := s.Selections.Connections[parts[1]]
	switch parts[0] {
	case "tool":
		return fmt.Sprint(slices.Contains(sel.DisabledTools, parts[2]))
	case "input":
		if value, ok := sel.Inputs[parts[2]]; ok {
			return "set:" + value
		}
		return "unset"
	case "profile":
		return sel.CredentialProfile
	case "local":
		connection, ok := s.Personal.Connections[strings.TrimPrefix(parts[1], "local:")]
		if !ok {
			return "absent"
		}
		b, _ := json.Marshal(connection)
		return string(b)
	}
	return fmt.Sprintf("%t/%t", sel.Enabled, sel.ReviewRequired)
}

const (
	reasonChanged = "changed elsewhere"
	reasonStale   = "no longer applies"
	reasonPresent = "already in the configuration" // not dropped: the fresh state has the draft's value
)

// Dropped names a draft change a reload could not keep, and why.
type Dropped struct{ Change, Reason string }

// Draft holds unsaved changes as a journal on top of a base state.
type Draft struct {
	base, state config.State
	effective   config.EffectiveConfig
	ops         []op // journal, in the order the user made the changes
}

func NewDraft(base config.State) (*Draft, error) {
	d := &Draft{}
	if err := d.reset(base); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *Draft) reset(base config.State) error {
	b, err := config.CloneState(base)
	if err != nil {
		return err
	}
	s, err := config.CloneState(base)
	if err != nil {
		return err
	}
	effective, err := config.Resolve(s)
	if err != nil {
		return err
	}
	d.base, d.state, d.effective, d.ops = b, s, effective, nil
	return nil
}

func (d *Draft) Base() config.State                { return d.base }
func (d *Draft) State() config.State               { return d.state }
func (d *Draft) Effective() config.EffectiveConfig { return d.effective }

// Changes counts journal fields whose draft value differs from the base.
func (d *Draft) Changes() int {
	seen := map[string]bool{}
	n := 0
	for _, o := range d.ops {
		key := o.key()
		if seen[key] {
			continue
		}
		seen[key] = true
		if fingerprint(d.state, key) != fingerprint(d.base, key) {
			n++
		}
	}
	return n
}

// Pending reports whether the draft changes the connection's execution
// configuration, so the runtime would not yet use what setup shows. Changes
// that cancel out do not count, nor do tool toggles: they only filter what a
// connection exposes.
func (d *Draft) Pending(id string) bool {
	return slices.ContainsFunc(d.ops, func(o op) bool {
		return o.id == id && o.kind != opTool && fingerprint(d.state, o.key()) != fingerprint(d.base, o.key())
	})
}

// errLocalExists rejects adding a personal ID that is already defined.
var errLocalExists = errors.New("personal connection already exists")

func (d *Draft) SetEnabled(id string, enabled bool) error {
	return d.record(op{kind: opEnabled, id: id, on: enabled})
}

// SetTool stages `tools enable|disable <id> <tool>`.
func (d *Draft) SetTool(id, tool string, enabled bool) error {
	return d.record(op{kind: opTool, id: id, name: tool, on: enabled})
}

// SetInput stages `config input set <id> <name> <value>`.
func (d *Draft) SetInput(id, name, value string) error {
	return d.record(op{kind: opInput, id: id, name: name, value: value})
}

// BindProfile stages `config profile bind <id> <profile>`.
func (d *Draft) BindProfile(id, profile string) error {
	return d.record(op{kind: opProfile, id: id, value: profile})
}

// AddLocal stages `local add --file` with the same definition bytes and
// returns the new canonical ID. An ID that is already defined is refused.
func (d *Draft) AddLocal(definition []byte) (string, error) {
	id, _, err := edit.DecodeLocalDefinition(definition, d.state.Personal)
	if err != nil {
		return "", err
	}
	if _, exists := d.state.Personal.Connections[id]; exists {
		return "", errLocalExists
	}
	canonical := "local:" + id
	return canonical, d.record(op{kind: opLocalAdd, id: canonical, raw: slices.Clone(definition)})
}

// UpdateLocal stages `local update <id> --file` for a canonical local ID.
func (d *Draft) UpdateLocal(id string, definition []byte) error {
	return d.record(op{kind: opLocalUpdate, id: id, raw: slices.Clone(definition)})
}

func (d *Draft) record(o op) error {
	next, err := config.CloneState(d.state)
	if err != nil {
		return err
	}
	if err := o.apply(&next); err != nil {
		return err
	}
	effective, err := config.Resolve(next)
	if err != nil {
		return err
	}
	d.ops = append(d.ops, o)
	d.state, d.effective = next, effective
	if d.Changes() == 0 {
		return d.reset(d.base)
	}
	return nil
}

// saveFunc performs a prepared save without touching the draft, so it can run
// as a Bubble Tea command while the model keeps rendering.
type saveFunc func(ctx context.Context, store *config.Store) (config.State, error)

// prepareSave captures the journal, the accepted IDs and the base revision.
// It returns nil when there is nothing to save.
func (d *Draft) prepareSave() saveFunc {
	if d.Changes() == 0 {
		return nil
	}
	last := map[string]bool{}
	for _, o := range d.ops {
		if o.kind == opEnabled {
			last[o.id] = o.on
		}
	}
	accept := []string{}
	for id, on := range last {
		row := d.effective.Connections[id]
		if on && row.Enabled && row.Available && !row.ReviewRequired && !slices.Contains(row.Blockers, "config_required") {
			accept = append(accept, id)
		}
	}
	slices.Sort(accept)
	ops := slices.Clone(d.ops)
	revision := d.base.Selections.Revision
	return func(ctx context.Context, store *config.Store) (config.State, error) {
		return store.UpdateAccepted(ctx, revision, accept, func(s *config.State) error {
			for _, o := range ops {
				if err := o.apply(s); err != nil {
					return err
				}
			}
			return nil
		})
	}
}

// commit makes a saved state the new base and clears the journal.
func (d *Draft) commit(saved config.State) error { return d.reset(saved) }

// Save writes every change in one store update. With nothing to save it makes
// no store call. On error the draft is unchanged.
func (d *Draft) Save(ctx context.Context, store *config.Store) (config.State, error) {
	save := d.prepareSave()
	if save == nil {
		return config.CloneState(d.base)
	}
	next, err := save(ctx, store)
	if err != nil {
		return config.State{}, err
	}
	if err := d.commit(next); err != nil {
		return config.State{}, err
	}
	return next, nil
}

// Rebase moves the draft onto fresh. A change whose value fresh already holds
// leaves the journal (reasonPresent, e.g. after a save that landed but could not
// be confirmed). Changes to a field or definition that changed since the old base
// are dropped, as are changes that no longer apply; the rest are replayed in
// order.
func (d *Draft) Rebase(fresh config.State) ([]Dropped, error) {
	oldEffective, err := config.Resolve(d.base)
	if err != nil {
		return nil, err
	}
	freshEffective, err := config.Resolve(fresh)
	if err != nil {
		return nil, err
	}
	next, err := config.CloneState(fresh)
	if err != nil {
		return nil, err
	}
	definition := func(effective config.EffectiveConfig, id string) string {
		b, _ := json.Marshal(effective.Connections[id].Definition)
		return string(b)
	}
	var dropped []Dropped
	reported := map[string]bool{}
	drop := func(o op, reason string) {
		if !reported[o.key()+"\x00"+reason] {
			reported[o.key()+"\x00"+reason] = true
			dropped = append(dropped, Dropped{Change: o.describe(oldEffective), Reason: reason})
		}
	}
	kept := []op{}
	for _, o := range d.ops {
		key := o.key()
		if want := fingerprint(d.state, key); want != fingerprint(d.base, key) && want == fingerprint(fresh, key) {
			drop(o, reasonPresent)
			continue
		}
		if fingerprint(d.base, key) != fingerprint(fresh, key) || definition(oldEffective, o.id) != definition(freshEffective, o.id) {
			drop(o, reasonChanged)
			continue
		}
		trial, err := config.CloneState(next)
		if err != nil {
			return nil, err
		}
		if err := o.apply(&trial); err != nil {
			drop(o, reasonStale)
			continue
		}
		if _, err := config.Resolve(trial); err != nil {
			drop(o, reasonStale)
			continue
		}
		next = trial
		kept = append(kept, o)
	}
	if err := d.reset(fresh); err != nil {
		return nil, err
	}
	effective, err := config.Resolve(next)
	if err != nil {
		return nil, err
	}
	d.state, d.effective, d.ops = next, effective, kept
	if d.Changes() == 0 {
		return dropped, d.reset(fresh)
	}
	return dropped, nil
}

func rowLabel(row config.EffectiveConnection) string {
	if row.Definition != nil && row.Definition.Label != "" {
		return output.DisplayMetadata(row.Definition.Label)
	}
	return output.DisplayMetadata(row.ID)
}
