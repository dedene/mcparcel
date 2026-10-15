package config

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// JoinMinVersion is the first MCParcel that knows the join fields: presets
// and credential profile suggestions. Older versions reject a catalog that
// has them as invalid, so a catalog that uses them must set minVersion to
// this version or newer; a prerelease of it counts.
const JoinMinVersion = "0.2.0"

// joinFloor ranks below every prerelease of JoinMinVersion.
const joinFloor = JoinMinVersion + "-0"

const (
	maxPresets           = 32
	maxPresetLabel       = 64
	maxPresetDescription = 200
	maxAccount           = 253
)

// accountGrammar is a 1Password sign-in address: a lowercase DNS host name
// with at least two labels, such as example.1password.com. An account ID or
// an email address is not accepted as a suggestion.
var accountGrammar = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidSuggestedAccount reports whether account is a 1Password sign-in
// address as a catalog suggestion must be. mcparcel join holds another
// account the teammate types to the same rule.
func ValidSuggestedAccount(account string) bool {
	return len(account) <= maxAccount && accountGrammar.MatchString(account)
}

// validateSuggestion accepts only a mode that needs no secret, so a catalog
// can never propose a token, a reference or a service account.
func validateSuggestion(s ProfileSuggestion, path string) error {
	if s.Mode != ProfileModeDesktop {
		return fieldError(path+".mode", "only desktop can be suggested")
	}
	if !ValidSuggestedAccount(s.Account) {
		return fieldError(path+".account", "1Password sign-in address required, such as example.1password.com")
	}
	return nil
}

func validatePresets(presets []Preset, connections map[string]Connection) error {
	if (presets != nil && len(presets) == 0) || len(presets) > maxPresets {
		return fieldError("presets", fmt.Sprintf("1 to %d presets required", maxPresets))
	}
	seen := make(map[string]bool, len(presets))
	for i, p := range presets {
		path := fmt.Sprintf("presets[%d]", i)
		if !identifier.MatchString(p.ID) || seen[p.ID] {
			return fieldError(path+".id", "invalid or duplicate identifier")
		}
		seen[p.ID] = true
		if p.Label == "" || !displayLine(p.Label, maxPresetLabel) {
			return fieldError(path+".label", fmt.Sprintf("nonempty label of at most %d characters required", maxPresetLabel))
		}
		if !displayLine(p.Description, maxPresetDescription) {
			return fieldError(path+".description", fmt.Sprintf("description of at most %d characters required", maxPresetDescription))
		}
		if len(p.Connections) == 0 {
			return fieldError(path+".connections", "at least one connection required")
		}
		listed := make(map[string]bool, len(p.Connections))
		for j, id := range p.Connections {
			if _, ok := connections[id]; !ok || listed[id] {
				return fieldError(fmt.Sprintf("%s.connections[%d]", path, j), "undeclared or duplicate connection")
			}
			listed[id] = true
		}
	}
	return nil
}

// displayLine reports whether text is at most limit characters without
// control characters, so it renders on one terminal line as written.
func displayLine(text string, limit int) bool {
	return utf8.RuneCountInString(text) <= limit && strings.IndexFunc(text, unicode.IsControl) < 0
}

// joinField returns the first join field c uses, or "".
func joinField(c Catalog) string {
	if c.Presets != nil {
		return "presets"
	}
	for _, id := range sortedKeys(c.CredentialProfiles) {
		if c.CredentialProfiles[id].Suggest != nil {
			return "credentialProfiles." + id + ".suggest"
		}
	}
	return ""
}

// checkJoinMinVersion requires minVersion JoinMinVersion or newer of a
// catalog that uses a join field.
func checkJoinMinVersion(c Catalog) error {
	field := joinField(c)
	if field == "" {
		return nil
	}
	if order, ok := CompareVersion(c.MinVersion, joinFloor); !ok || order < 0 {
		return fieldError("minVersion", JoinMinVersion+" or newer required by "+field)
	}
	return nil
}

// personalOnly rejects in personal.json what only a GitHub catalog may have:
// minVersion and the join fields.
func personalOnly(c Personal) error {
	field := joinField(c)
	if c.MinVersion != "" {
		field = "minVersion"
	}
	if field != "" {
		return fieldError("personal."+field, "allowed only in a GitHub catalog")
	}
	return nil
}
