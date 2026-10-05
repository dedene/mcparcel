package config

import "errors"

var ErrImportBlocked = errors.New("import has blocked entries")

type ImportBlockedError struct {
	Report ImportReport `json:"report"`
}

func (e *ImportBlockedError) Error() string { return "import has blocked entries" }
func (e *ImportBlockedError) Unwrap() error { return ErrImportBlocked }

type CredentialBinding struct {
	Profile string `json:"profile"`
	Secret  string `json:"secret"`
}
type ImportIssue struct {
	Path     string `json:"path"`
	Code     string `json:"code"`
	Variable string `json:"variable,omitempty"`
}
type ImportEntry struct {
	ID                string        `json:"id"`
	CanonicalID       string        `json:"canonicalId"`
	Alias             string        `json:"alias"`
	Transport         string        `json:"transport"`
	OAuth             bool          `json:"oauth"`
	Applicable        bool          `json:"applicable"`
	Selected          bool          `json:"selected"`
	Applied           bool          `json:"applied"`
	CredentialProfile string        `json:"credentialProfile,omitempty"`
	Fields            []string      `json:"fields"`
	Unresolved        []ImportIssue `json:"unresolved"`
	Warnings          []ImportIssue `json:"warnings"`
}
type ImportReport struct {
	Entries     []ImportEntry     `json:"entries"`
	Definitions Catalog           `json:"definitions"`
	Aliases     map[string]string `json:"aliases"`
	Issues      []ImportIssue     `json:"issues"`
	Applied     bool              `json:"applied"`
	Revision    *uint64           `json:"revision,omitempty"`
}
