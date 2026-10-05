package config

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

func DecodePersonal(data []byte) (Personal, error) { return DecodeCatalog(data) }
func DecodeCatalog(data []byte) (Catalog, error) {
	var c Catalog
	if err := decodeStrict(data, &c, "catalog"); err != nil {
		return Catalog{}, err
	}
	if c.SchemaVersion != 1 {
		return Catalog{}, fieldError("schemaVersion", "unsupported version")
	}
	if c.Domains == nil {
		c.Domains = make(map[string]Domain)
	}
	if c.CredentialProfiles == nil {
		c.CredentialProfiles = make(map[string]ProfileRequirement)
	}
	for _, id := range sortedKeys(c.Domains) {
		if !identifier.MatchString(id) || id == "other" {
			return Catalog{}, fieldError("domains", "invalid identifier")
		}
		if c.Domains[id].Label == "" || strings.ContainsRune(c.Domains[id].Label, 0) {
			return Catalog{}, fieldError("domains."+id+".label", "nonempty label required")
		}
	}
	for _, id := range sortedKeys(c.CredentialProfiles) {
		if !identifier.MatchString(id) {
			return Catalog{}, fieldError("credentialProfiles", "invalid identifier")
		}
	}
	for _, id := range sortedKeys(c.Connections) {
		if !identifier.MatchString(id) {
			return Catalog{}, fieldError("connections", "invalid identifier")
		}
		conn := c.Connections[id]
		path := "connections." + id
		for _, domain := range conn.Domains {
			if _, ok := c.Domains[domain]; !ok || domain == "other" {
				return Catalog{}, fieldError(path+".domains", "undeclared domain")
			}
		}
		if err := validateConnection(&conn, path, c.CredentialProfiles); err != nil {
			return Catalog{}, err
		}
		c.Connections[id] = conn
	}
	return c, nil
}

func validateConnection(c *Connection, path string, profiles map[string]ProfileRequirement) error {
	if c.CallTimeout == "" {
		c.CallTimeout = "120s"
	}
	if d, err := time.ParseDuration(c.CallTimeout); err != nil || d <= 0 {
		return fieldError(path+".callTimeout", "positive duration required")
	}
	if c.CredentialProfile != "" {
		if _, ok := profiles[c.CredentialProfile]; !ok {
			return fieldError(path+".credentialProfile", "undeclared requirement")
		}
	}
	for _, id := range sortedKeys(c.Inputs) {
		input := c.Inputs[id]
		p := path + ".inputs." + id
		if !identifier.MatchString(id) {
			return fieldError(path+".inputs", "invalid identifier")
		}
		if input.Kind != "string" && input.Kind != "path" && input.Kind != "url" {
			return fieldError(p+".kind", "invalid input kind")
		}
		if input.Description == "" || strings.ContainsRune(input.Description, 0) {
			return fieldError(p+".description", "nonempty description required")
		}
		if input.Default != nil {
			if err := validateInputText(*input.Default, input.Kind, p+".default"); err != nil {
				return err
			}
		}
	}
	if (c.Transport.Stdio == nil) == (c.Transport.HTTP == nil) {
		return fieldError(path+".transport", "one transport required")
	}
	value := func(v Value, p string, header, secret bool) error {
		if v.Secret != nil && !secret {
			return fieldError(p, "secret not allowed")
		}
		if v.Input != nil {
			if _, ok := c.Inputs[v.Input.Input]; !ok {
				return fieldError(p+".input", "undeclared input")
			}
			return nil
		}
		return validateValue(v, p, header)
	}
	if s := c.Transport.Stdio; s != nil {
		p := path + ".transport"
		if err := value(s.Command, p+".command", false, false); err != nil {
			return err
		}
		if s.Command.Literal != nil && (*s.Command.Literal == "" || strings.Contains(*s.Command.Literal, "op://")) {
			return fieldError(p+".command", "literal command required")
		}
		for i, v := range s.Args {
			q := fmt.Sprintf("%s.args[%d]", p, i)
			if err := value(v, q, false, false); err != nil {
				return err
			}
			if v.Literal != nil && strings.Contains(*v.Literal, "op://") {
				return fieldError(q, "literal arguments required")
			}
		}
		if s.Cwd != nil {
			if err := value(*s.Cwd, p+".cwd", false, false); err != nil {
				return err
			}
			if s.Cwd.Literal != nil && (!filepath.IsAbs(*s.Cwd.Literal) || strings.Contains(*s.Cwd.Literal, "op://")) {
				return fieldError(p+".cwd", "absolute literal path required")
			}
		}
		for _, name := range s.InheritEnv {
			if !envName.MatchString(name) || ProtectedEnv(name) {
				return fieldError(p+".inheritEnv", "invalid or protected environment name")
			}
		}
		for _, name := range sortedKeys(s.Env) {
			if !envName.MatchString(name) || forbiddenExplicit(name) {
				return fieldError(p+".env", "invalid or protected environment name")
			}
			if err := value(s.Env[name], p+".env."+name, false, true); err != nil {
				return err
			}
		}
	} else {
		h := c.Transport.HTTP
		p := path + ".transport"
		if h.Mode == "" {
			h.Mode = "auto"
		}
		if h.AllowInsecureHTTP == "" {
			h.AllowInsecureHTTP = "never"
		}
		if h.Mode != "auto" && h.Mode != "streamable" && h.Mode != "sse" {
			return fieldError(p+".mode", "invalid HTTP mode")
		}
		if h.AllowInsecureHTTP != "never" && h.AllowInsecureHTTP != "loopback" && h.AllowInsecureHTTP != "explicit" {
			return fieldError(p+".allowInsecureHttp", "invalid consent")
		}
		if err := value(h.URL, p+".url", false, false); err != nil {
			return err
		}
		if h.URL.Literal != nil {
			u, err := parseHTTPURL(*h.URL.Literal)
			if err != nil || strings.Contains(*h.URL.Literal, "op://") {
				return fieldError(p+".url", "invalid literal HTTP URL")
			}
			if u.Scheme == "http" && (h.AllowInsecureHTTP == "never" || h.AllowInsecureHTTP == "loopback" && !loopbackHost(u.Hostname())) {
				return fieldError(p+".allowInsecureHttp", "HTTP consent required")
			}
		}
		seen := make(map[string]bool)
		for _, name := range sortedKeys(h.Headers) {
			lower := strings.ToLower(name)
			if !headerName.MatchString(name) || seen[lower] {
				return fieldError(p+".headers", "invalid or duplicate header name")
			}
			seen[lower] = true
			if c.Auth != nil && lower == "authorization" {
				return fieldError(p+".headers", "Authorization conflicts with OAuth")
			}
			if err := value(h.Headers[name], p+".headers."+name, true, true); err != nil {
				return err
			}
		}
	}
	if a := c.Auth; a != nil {
		p := path + ".auth"
		if c.Transport.HTTP == nil {
			return fieldError(p, "OAuth requires HTTP")
		}
		if a.Type != "oauth" {
			return fieldError(p+".type", "invalid auth type")
		}
		if strings.ContainsRune(a.ClientName, 0) {
			return fieldError(p+".clientName", "invalid characters")
		}
		if a.TokenEndpointAuthMethod != "" && a.TokenEndpointAuthMethod != "none" && a.TokenEndpointAuthMethod != "client_secret_basic" && a.TokenEndpointAuthMethod != "client_secret_post" {
			return fieldError(p+".tokenEndpointAuthMethod", "invalid token method")
		}
		for _, scope := range a.Scopes {
			if scope == "" {
				return fieldError(p+".scopes", "invalid scope token")
			}
			for _, ch := range scope {
				if ch <= 32 || ch >= 127 || ch == '"' || ch == '\\' {
					return fieldError(p+".scopes", "invalid scope token")
				}
			}
		}
		if a.RedirectURL != "" {
			u, err := parseHTTPURL(a.RedirectURL)
			if err != nil || u.Scheme == "http" && !loopbackHost(u.Hostname()) {
				return fieldError(p+".redirectUrl", "HTTPS or loopback HTTP required")
			}
		}
		if a.IssuerURL != "" {
			u, err := parseHTTPURL(a.IssuerURL)
			if err != nil || u.Scheme != "https" {
				return fieldError(p+".issuerUrl", "HTTPS required")
			}
		}
		if a.ClientID != nil {
			if err := value(*a.ClientID, p+".clientId", false, true); err != nil {
				return err
			}
		}
		if a.ClientSecret != nil {
			if a.ClientSecret.Secret == nil {
				return fieldError(p+".clientSecret", "secret reference required")
			}
			if err := value(*a.ClientSecret, p+".clientSecret", false, true); err != nil {
				return err
			}
		}
	}
	if policy := c.ToolPolicy; policy != nil {
		if policy.Allow != nil {
			if err := validateToolNames(*policy.Allow, path+".toolPolicy.allow"); err != nil {
				return err
			}
		}
		if err := validateToolNames(policy.Deny, path+".toolPolicy.deny"); err != nil {
			return err
		}
	}
	if l := c.Lifecycle; l != nil && l.IdleTimeout != "" && l.IdleTimeout != "session" {
		if d, err := time.ParseDuration(l.IdleTimeout); err != nil || d <= 0 {
			return fieldError(path+".lifecycle.idleTimeout", "positive duration or session required")
		}
	}
	if len(SecretRefs(*c)) > 0 && c.CredentialProfile == "" {
		return fieldError(path+".credentialProfile", "secret binding requires profile")
	}
	return nil
}

func validateToolNames(names []string, path string) error {
	seen := make(map[string]bool)
	for _, name := range names {
		if name == "" || strings.ContainsRune(name, 0) || seen[name] {
			return fieldError(path, "empty, invalid or duplicate tool name")
		}
		seen[name] = true
	}
	return nil
}

func validateValue(v Value, path string, header bool) error {
	text := ""
	if v.Literal != nil {
		text = *v.Literal
	} else if v.Secret != nil {
		if !validValueRef(v.Secret.Secret) {
			return fieldError(path+".secret", "invalid secret reference")
		}
		text = v.Secret.Prefix + v.Secret.Suffix
	} else {
		return fieldError(path, "value required")
	}
	if strings.ContainsRune(text, 0) || header && strings.ContainsAny(text, "\r\n") {
		return fieldError(path, "invalid value characters")
	}
	return nil
}

func parseHTTPURL(text string) (*url.URL, error) {
	u, err := url.Parse(text)
	if err != nil || strings.ContainsAny(text, "\x00#") || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.Hostname() == "" || u.Scheme != "http" && u.Scheme != "https" {
		return nil, ErrConfig
	}
	return u, nil
}

func loopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()
}

func validateInputText(text, kind, path string) error {
	if strings.ContainsRune(text, 0) {
		return fieldError(path, "invalid input characters")
	}
	switch kind {
	case "path":
		if !filepath.IsAbs(text) {
			return fieldError(path, "absolute path required")
		}
	case "url":
		if _, err := parseHTTPURL(text); err != nil {
			return fieldError(path, "HTTP URL required")
		}
	}
	return nil
}
