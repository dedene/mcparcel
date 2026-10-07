package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"sort"
	"strings"
	"time"
)

const maxConfigBytes = 2 * 1024 * 1024

func Load(paths Paths) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	state, err := ReadState(ctx, paths)
	if err != nil {
		return Snapshot{}, err
	}
	effective, err := Resolve(state)
	if err != nil {
		return Snapshot{}, err
	}
	if len(effective.Connections) == 0 {
		return Snapshot{}, ErrConfigRequired
	}
	s := Snapshot{Personal: state.Personal, Local: state.Local, Effective: &effective, Revision: effective.Revision}
	s.Hash, err = hashJSON([]any{state.Local, state.Personal, state.Selections, state.Catalogs})
	if state.Legacy {
		s.Effective.SourceRevisions = map[string]string{"personal": s.Hash}
	}
	return s, err
}

func readConfig(path string) ([]byte, error) {
	f, err := OpenConfigFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return nil, fieldError("file", "could not read configuration")
	}
	if len(data) > maxConfigBytes {
		return nil, fieldError("file", "configuration exceeds 2097152 bytes")
	}
	return data, nil
}

func hashJSON(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", fieldError("snapshot", "invalid configuration")
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (s Snapshot) effectiveConfig() (EffectiveConfig, error) {
	if s.Effective != nil {
		return *s.Effective, nil
	}
	state := legacyState(s.Personal, s.Local)
	return Resolve(state)
}

func (s Snapshot) Connection(id string) (string, Connection, error) {
	effective, err := s.effectiveConfig()
	if err != nil {
		return "", Connection{}, err
	}
	ids := sortedKeys(effective.Connections)
	canonical, err := ResolveID(id, effective.Aliases, ids)
	if err != nil {
		return "", Connection{}, err
	}
	row := effective.Connections[canonical]
	if !row.Available {
		return "", Connection{}, ErrNotFound
	}
	if !row.Enabled {
		return "", Connection{}, ErrDisabled
	}
	if row.ReviewRequired {
		return "", Connection{}, ErrReviewRequired
	}
	if slices.Contains(row.Blockers, "config_required") {
		return "", Connection{}, ErrConfigRequired
	}
	c := cloneConnection(*row.Connection)
	policy := row.Policy
	c.ToolPolicy = &policy
	return canonical, cloneConnection(c), nil
}

func (s Snapshot) ConnectionHash(id string) (string, error) {
	canonical, c, err := s.Connection(id)
	if err != nil {
		return "", err
	}
	var profile *Profile
	if c.CredentialProfile != "" {
		p := s.Local.CredentialProfiles[c.CredentialProfile]
		profile = &p
	}
	idle := "session"
	if c.Lifecycle != nil && c.Lifecycle.IdleTimeout != "" {
		idle = c.Lifecycle.IdleTimeout
	}
	return hashJSON(map[string]any{"id": canonical, "transport": c.Transport, "auth": c.Auth, "toolPolicy": c.ToolPolicy, "idleTimeout": idle, "callTimeout": c.CallTimeout, "credentialProfile": c.CredentialProfile, "profile": profile})
}

func (s Snapshot) CheckTool(id, tool string) error {
	_, c, err := s.Connection(id)
	if err != nil {
		return err
	}
	if !ToolAllowed(*c.ToolPolicy, tool) {
		return ErrToolDenied
	}
	return nil
}

var ErrRuntimeUnsupported = errors.New("runtime feature unavailable")

func (s Snapshot) RuntimeConnection(id string) (string, Connection, error) {
	canonical, c, err := s.Connection(id)
	if err != nil {
		return "", Connection{}, err
	}
	if c.Transport.HTTP != nil && c.Transport.HTTP.Mode == "sse" || c.Lifecycle != nil && c.Lifecycle.IdleTimeout != "" && c.Lifecycle.IdleTimeout != "session" {
		return "", Connection{}, ErrRuntimeUnsupported
	}
	return canonical, c, nil
}

// SecretRefs returns 1Password references; EnvRefs returns the variable names
// of env: references. Anything with the env: prefix is never a 1Password ref.
func SecretRefs(c Connection) []string { return valueRefs(c, false) }
func EnvRefs(c Connection) []string    { return valueRefs(c, true) }

func valueRefs(c Connection, wantEnv bool) []string {
	set := make(map[string]bool)
	add := func(v Value) {
		if v.Secret == nil {
			return
		}
		name, env := strings.CutPrefix(v.Secret.Secret, "env:")
		if env != wantEnv {
			return
		}
		if env {
			set[name] = true
		} else {
			set[v.Secret.Secret] = true
		}
	}
	if c.Transport.Stdio != nil {
		for _, v := range c.Transport.Stdio.Env {
			add(v)
		}
	}
	if c.Transport.HTTP != nil {
		for _, v := range c.Transport.HTTP.Headers {
			add(v)
		}
	}
	if c.Auth != nil {
		for _, v := range []*Value{c.Auth.ClientID, c.Auth.ClientSecret} {
			if v != nil {
				add(*v)
			}
		}
	}
	refs := make([]string, 0, len(set))
	for ref := range set {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}
