package config

type (
	Domain struct {
		Label string `json:"label"`
	}
	Input struct {
		Kind        string  `json:"kind"`
		Description string  `json:"description"`
		Default     *string `json:"default,omitempty"`
	}
)

type (
	// ProfileRequirement is a credential profile a catalog's connections
	// need. Suggest is a GitHub catalog's proposal for the local profile that
	// meets it; the teammate confirms it before anything is created.
	ProfileRequirement struct {
		Description string             `json:"description,omitempty"`
		Suggest     *ProfileSuggestion `json:"suggest,omitempty"`
	}
	// ProfileSuggestion names a mode that needs no secret (only desktop) and
	// the 1Password account's sign-in address. Never a token, reference or
	// service account.
	ProfileSuggestion struct {
		Mode    string `json:"mode"`
		Account string `json:"account"`
	}
	// Preset is a named starting selection, such as a role: connection IDs of
	// the same catalog, in the catalog's order.
	Preset struct {
		ID          string   `json:"id"`
		Label       string   `json:"label"`
		Description string   `json:"description,omitempty"`
		Connections []string `json:"connections"`
	}
	ToolPolicy struct {
		Allow *[]string `json:"allow,omitempty"`
		Deny  []string  `json:"deny,omitempty"`
	}
)

type (
	// Lifecycle.KeepAlive is how often the daemon refreshes an idle OAuth
	// session: "off" or a duration of at least 1h; empty means 24h.
	Lifecycle struct {
		IdleTimeout string `json:"idleTimeout,omitempty"`
		KeepAlive   string `json:"keepAlive,omitempty"`
	}
	// OAuth.Grant is GrantAuthorizationCode (also when empty) or
	// GrantClientCredentials; TokenURL is the client_credentials token
	// endpoint, used without discovery.
	OAuth struct {
		Type                    string   `json:"type"`
		Grant                   string   `json:"grant,omitempty"`
		TokenURL                string   `json:"tokenUrl,omitempty"`
		ClientName              string   `json:"clientName,omitempty"`
		Scopes                  []string `json:"scopes,omitempty"`
		ClientID                *Value   `json:"clientId,omitempty"`
		ClientSecret            *Value   `json:"clientSecret,omitempty"`
		TokenEndpointAuthMethod string   `json:"tokenEndpointAuthMethod,omitempty"`
		RedirectURL             string   `json:"redirectUrl,omitempty"`
		IssuerURL               string   `json:"issuerUrl,omitempty"`
	}
)

// OAuth grants. An empty OAuth.Grant means GrantAuthorizationCode.
const (
	GrantAuthorizationCode = "authorization_code"
	GrantClientCredentials = "client_credentials"
)

type Connection struct {
	Label             string           `json:"label,omitempty"`
	Description       string           `json:"description,omitempty"`
	Domains           []string         `json:"domains,omitempty"`
	Inputs            map[string]Input `json:"inputs,omitempty"`
	CredentialProfile string           `json:"credentialProfile,omitempty"`
	Transport         Transport        `json:"transport"`
	Auth              *OAuth           `json:"auth,omitempty"`
	ToolPolicy        *ToolPolicy      `json:"toolPolicy,omitempty"`
	Lifecycle         *Lifecycle       `json:"lifecycle,omitempty"`
	CallTimeout       string           `json:"callTimeout,omitempty"`
	StartupTimeout    string           `json:"startupTimeout,omitempty"`
}
type Catalog struct {
	SchemaVersion int `json:"schemaVersion"`
	// MinVersion is the oldest MCParcel that may add or sync this catalog
	// (semver, no build metadata). GitHub catalogs only, not personal.json.
	MinVersion         string                        `json:"minVersion,omitempty"`
	Name               string                        `json:"name,omitempty"`
	Domains            map[string]Domain             `json:"domains,omitempty"`
	CredentialProfiles map[string]ProfileRequirement `json:"credentialProfiles,omitempty"`
	// Presets are offered when joining, in this order. GitHub catalogs only.
	Presets     []Preset              `json:"presets,omitempty"`
	Connections map[string]Connection `json:"connections"`
}

// Profile is the local profile a teammate gets by accepting the suggestion.
func (s ProfileSuggestion) Profile() Profile { return Profile{Mode: s.Mode, Account: s.Account} }

type Personal = Catalog
