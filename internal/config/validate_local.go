package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	ownerGrammar  = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,37}[a-z0-9])?$`)
	repoGrammar   = regexp.MustCompile(`^[a-z0-9._-]{1,100}$`)
	commitGrammar = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

func validOwner(owner string) bool { return ownerGrammar.MatchString(owner) }
func validRepo(repo string) bool   { return repo != "." && repo != ".." && repoGrammar.MatchString(repo) }

func DecodeLocal(data []byte) (Local, error) {
	var l Local
	if err := decodeStrict(data, &l, "local"); err != nil {
		return Local{}, err
	}
	if l.SchemaVersion != 1 {
		return Local{}, fieldError("schemaVersion", "unsupported version")
	}
	if l.CredentialProfiles == nil {
		l.CredentialProfiles = make(map[string]Profile)
	}
	if l.Aliases == nil {
		l.Aliases = make(map[string]string)
	}
	if err := validateRuntime(l.Runtime); err != nil {
		return Local{}, err
	}
	for _, id := range sortedKeys(l.CredentialProfiles) {
		p := l.CredentialProfiles[id]
		path := "credentialProfiles." + id
		if !identifier.MatchString(id) {
			return Local{}, fieldError("credentialProfiles", "invalid identifier")
		}
		if p.Mode != "desktop-service-account" && p.Mode != "desktop" {
			return Local{}, fieldError(path+".mode", "invalid profile mode")
		}
		if p.Account == "" || strings.ContainsRune(p.Account, 0) {
			return Local{}, fieldError(path+".account", "nonempty account required")
		}
		if p.Mode == "desktop-service-account" && !validRef(p.BootstrapRef) {
			return Local{}, fieldError(path+".bootstrapRef", "invalid secret reference")
		}
		if p.Mode == "desktop" && p.BootstrapRef != "" {
			return Local{}, fieldError(path+".bootstrapRef", "desktop forbids bootstrapRef")
		}
		if p.SessionDuration == "" {
			p.SessionDuration = "24h"
		}
		d, err := time.ParseDuration(p.SessionDuration)
		if err != nil || d <= 0 || d > 24*time.Hour {
			return Local{}, fieldError(path+".sessionDuration", "must be positive and at most 24h")
		}
		l.CredentialProfiles[id] = p
	}
	ids := make(map[string]bool)
	nums := make(map[uint64]bool)
	names := make(map[string]bool)
	for i, s := range l.Sources {
		path := fmt.Sprintf("sources[%d]", i)
		if s.RepositoryID == 0 || s.RepositoryID > MaxRevision {
			return Local{}, fieldError(path+".repositoryId", "positive interoperable repository ID required")
		}
		if s.ID != "github-"+strconv.FormatUint(s.RepositoryID, 10) {
			return Local{}, fieldError(path+".id", "repository ID source required")
		}
		if !validOwner(s.Owner) {
			return Local{}, fieldError(path+".owner", "canonical GitHub owner required")
		}
		if !validRepo(s.Repo) {
			return Local{}, fieldError(path+".repo", "canonical GitHub repository required")
		}
		if !validSourcePath(s.Path) {
			return Local{}, fieldError(path+".path", "repository-relative path required")
		}
		if s.Ref == "" || strings.ContainsRune(s.Ref, 0) {
			return Local{}, fieldError(path+".ref", "nonempty reference required")
		}
		if !commitGrammar.MatchString(s.Commit) {
			return Local{}, fieldError(path+".commit", "40 lowercase hex digits required")
		}
		if ids[s.ID] || nums[s.RepositoryID] || names[s.Owner+"/"+s.Repo] {
			return Local{}, fieldError(path, "duplicate source")
		}
		ids[s.ID], nums[s.RepositoryID], names[s.Owner+"/"+s.Repo] = true, true, true
	}
	for _, alias := range sortedKeys(l.Aliases) {
		if !identifier.MatchString(alias) {
			return Local{}, fieldError("aliases", "invalid identifier")
		}
		if ValidateCanonicalID(l.Aliases[alias]) != nil {
			return Local{}, fieldError("aliases."+alias, "canonical connection ID required")
		}
	}
	return l, nil
}

func validSourcePath(path string) bool {
	if strings.ContainsAny(path, "\\\x00") {
		return false
	}
	for _, component := range strings.Split(path, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}
