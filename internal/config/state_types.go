package config

const MaxRevision uint64 = 9007199254740991

type Profile struct {
	Mode            string `json:"mode"`
	Account         string `json:"account"`
	BootstrapRef    string `json:"bootstrapRef,omitempty"`
	SessionDuration string `json:"sessionDuration,omitempty"`
}
type Source struct {
	ID           string `json:"id"`
	RepositoryID uint64 `json:"repositoryId"`
	Owner        string `json:"owner"`
	Repo         string `json:"repo"`
	Path         string `json:"path"`
	Ref          string `json:"ref"`
	Commit       string `json:"commit"`
	Pinned       bool   `json:"pinned"`
}
type RuntimeDefaults struct {
	KeepAlive bool `json:"keepAlive,omitempty"`
}
type Local struct {
	SchemaVersion      int                `json:"schemaVersion"`
	Sources            []Source           `json:"sources,omitempty"`
	CredentialProfiles map[string]Profile `json:"credentialProfiles,omitempty"`
	Aliases            map[string]string  `json:"aliases,omitempty"`
	Runtime            *RuntimeDefaults   `json:"runtime,omitempty"`
}
type Selection struct {
	Enabled           bool              `json:"enabled"`
	Inputs            map[string]string `json:"inputs,omitempty"`
	CredentialProfile string            `json:"credentialProfile,omitempty"`
	DisabledTools     []string          `json:"disabledTools,omitempty"`
	ReviewRequired    bool              `json:"reviewRequired,omitempty"`
}
type Selections struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Revision      uint64               `json:"revision"`
	Connections   map[string]Selection `json:"connections"`
}
type State struct {
	Local      Local              `json:"-"`
	Personal   Catalog            `json:"-"`
	Selections Selections         `json:"-"`
	Catalogs   map[string]Catalog `json:"-"`
	Legacy     bool               `json:"-"`
}
