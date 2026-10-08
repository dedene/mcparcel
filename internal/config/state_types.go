package config

const MaxRevision uint64 = 9007199254740991

// Credential profile modes. The desktop modes bootstrap through the 1Password
// desktop app; service-account reads a service-account token from tokenEnv or
// tokenFile and never prompts.
const (
	ProfileModeDesktopServiceAccount = "desktop-service-account"
	ProfileModeDesktop               = "desktop"
	ProfileModeServiceAccount        = "service-account"
)

type Profile struct {
	Mode            string `json:"mode"`
	Account         string `json:"account,omitempty"`
	BootstrapRef    string `json:"bootstrapRef,omitempty"`
	TokenEnv        string `json:"tokenEnv,omitempty"`
	TokenFile       string `json:"tokenFile,omitempty"`
	SessionDuration string `json:"sessionDuration,omitempty"`
}

// PromptFree reports whether the profile bootstraps without any prompt (a
// service-account token), so --no-input and headless mode can use it.
func (p Profile) PromptFree() bool { return p.Mode == ProfileModeServiceAccount }

// UsesDesktopApp reports whether the profile bootstraps through the 1Password
// desktop app.
func (p Profile) UsesDesktopApp() bool {
	return p.Mode == ProfileModeDesktop || p.Mode == ProfileModeDesktopServiceAccount
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
	KeepAlive      bool   `json:"keepAlive,omitempty"`
	ApprovalDialog bool   `json:"approvalDialog,omitempty"`
	Mode           string `json:"mode,omitempty"`      // "" = desktop
	StateRoot      string `json:"stateRoot,omitempty"` // required iff headless
	// Supervised (headless only) declares that mcparcel runtime serve owns
	// the runtime, so a CLI never auto-starts one, also before serve ran.
	Supervised bool `json:"supervised,omitempty"`
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
