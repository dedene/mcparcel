package config

import (
	"errors"
	"slices"
	"strings"
)

var (
	ErrAmbiguousID    = errors.New("ambiguous connection ID")
	ErrAliasCollision = errors.New("alias collision")
	ErrDisabled       = errors.New("connection disabled")
	ErrReviewRequired = errors.New("connection review required")
	ErrToolDenied     = errors.New("tool denied")
)

type AmbiguousIDError struct {
	Candidates []string `json:"candidates"`
}

func (e *AmbiguousIDError) Error() string { return "ambiguous connection ID" }
func (e *AmbiguousIDError) Unwrap() error { return ErrAmbiguousID }
func canonicalID(id string) bool {
	if tail, ok := strings.CutPrefix(id, "local:"); ok {
		return identifier.MatchString(tail)
	}
	tail, ok := strings.CutPrefix(id, "github:")
	if !ok {
		return false
	}
	repository, connection, ok := strings.Cut(tail, "#")
	if !ok || !identifier.MatchString(connection) {
		return false
	}
	owner, repo, ok := strings.Cut(repository, "/")
	return ok && validOwner(owner) && validRepo(repo)
}

func ValidateCanonicalID(id string) error {
	if !canonicalID(id) {
		return ErrConfig
	}
	return nil
}
func CanonicalLocal(id string) (string, error) { s := "local:" + id; return s, ValidateCanonicalID(s) }
func CanonicalGitHub(owner, repo, id string) (string, error) {
	s := "github:" + strings.ToLower(owner) + "/" + strings.ToLower(repo) + "#" + id
	return s, ValidateCanonicalID(s)
}

// ConnectionBase is a canonical ID's connection name: the part after
// "local:" or after "#".
func ConnectionBase(id string) string {
	if s, ok := strings.CutPrefix(id, "local:"); ok {
		return s
	}
	_, s, _ := strings.Cut(id, "#")
	return s
}

func AddAlias(aliases map[string]string, alias, target string) error {
	if !identifier.MatchString(alias) || ValidateCanonicalID(target) != nil {
		return ErrConfig
	}
	if old, exists := aliases[alias]; exists && old != target {
		return ErrAliasCollision
	}
	if aliases == nil {
		return ErrConfig
	}
	aliases[alias] = target
	return nil
}

func ResolveID(name string, aliases map[string]string, ids []string) (string, error) {
	exists := func(id string) bool { return slices.Contains(ids, id) }
	if strings.Contains(name, ":") {
		if ValidateCanonicalID(name) != nil {
			return "", ErrNotFound
		}
		if !exists(name) {
			return "", ErrNotFound
		}
		return name, nil
	}
	if !identifier.MatchString(name) {
		return "", ErrNotFound
	}
	if id, ok := aliases[name]; ok {
		if !exists(id) {
			return "", ErrNotFound
		}
		return id, nil
	}
	candidates := []string{}
	for _, id := range ids {
		if ConnectionBase(id) == name {
			candidates = append(candidates, id)
		}
	}
	slices.Sort(candidates)
	candidates = slices.Compact(candidates)
	switch len(candidates) {
	case 0:
		return "", ErrNotFound
	case 1:
		return candidates[0], nil
	default:
		return "", &AmbiguousIDError{Candidates: candidates}
	}
}
