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
	ProfileRequirement struct {
		Description string `json:"description,omitempty"`
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
	OAuth struct {
		Type                    string   `json:"type"`
		ClientName              string   `json:"clientName,omitempty"`
		Scopes                  []string `json:"scopes,omitempty"`
		ClientID                *Value   `json:"clientId,omitempty"`
		ClientSecret            *Value   `json:"clientSecret,omitempty"`
		TokenEndpointAuthMethod string   `json:"tokenEndpointAuthMethod,omitempty"`
		RedirectURL             string   `json:"redirectUrl,omitempty"`
		IssuerURL               string   `json:"issuerUrl,omitempty"`
	}
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
	SchemaVersion      int                           `json:"schemaVersion"`
	Name               string                        `json:"name,omitempty"`
	Domains            map[string]Domain             `json:"domains,omitempty"`
	CredentialProfiles map[string]ProfileRequirement `json:"credentialProfiles,omitempty"`
	Connections        map[string]Connection         `json:"connections"`
}
type Personal = Catalog
