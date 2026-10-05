package config

import (
	"net/url"
	"regexp"
	"strings"
)

var sourceReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func importValue(text, path string, bindings map[string]CredentialBinding) (Value, string, *ImportIssue) {
	matches := sourceReference.FindAllStringSubmatchIndex(text, -1)
	issue := func(code, variable string) (Value, string, *ImportIssue) {
		return Value{}, "", &ImportIssue{Path: path, Code: code, Variable: variable}
	}
	if len(matches) == 0 {
		if strings.Contains(text, "${") {
			return issue("invalid_reference", "")
		}
		return Literal(text), "", nil
	}
	if len(matches) != 1 {
		return issue("multiple_references", "")
	}
	m := matches[0]
	prefix, suffix := text[:m[0]], text[m[1]:]
	if strings.Contains(prefix+suffix, "${") {
		return issue("invalid_reference", "")
	}
	variable := text[m[2]:m[3]]
	binding, ok := bindings[variable]
	if !ok {
		return issue("unresolved_credential", variable)
	}
	if !identifier.MatchString(binding.Profile) || !validRef(binding.Secret) {
		return issue("invalid_binding", variable)
	}
	return Value{Secret: &SecretRef{Secret: binding.Secret, Prefix: prefix, Suffix: suffix}}, binding.Profile, nil
}

func DecodeBindings(raw []byte) (map[string]CredentialBinding, error) {
	var bindings map[string]CredentialBinding
	if err := decodeStrict(raw, &bindings, "bindings"); err != nil {
		return nil, err
	}
	if err := validateBindings(bindings); err != nil {
		return nil, err
	}
	return bindings, nil
}

func validateBindings(bindings map[string]CredentialBinding) error {
	for _, name := range sortedKeys(bindings) {
		b := bindings[name]
		if !envName.MatchString(name) || !identifier.MatchString(b.Profile) || !validRef(b.Secret) {
			return fieldError("bindings", "invalid binding")
		}
	}
	return nil
}

var (
	suspiciousEnv  = regexp.MustCompile(`(?i)(^|_)(API_KEY|ACCESS_KEY|TOKEN|SECRET|PASSWORD|PASSWD|CLIENT_SECRET)($|_)`)
	shellExpansion = regexp.MustCompile("\\$[A-Za-z_(]|`")
)

func protectedHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization", "x-api-key", "api-key":
		return true
	}
	return false
}

func suspiciousName(name string) bool {
	return !strings.EqualFold(name, "PROXMOX_TOKEN_NAME") && suspiciousEnv.MatchString(name)
}

func credentialFlag(text string) bool {
	name, _, _ := strings.Cut(strings.ToLower(text), "=")
	switch name {
	case "--token", "--api-key", "--access-key", "--secret", "--password", "--client-secret":
		return true
	}
	return false
}

func secretURL(text string) bool {
	u, e := url.Parse(text)
	if e != nil {
		return false
	}
	if u.User != nil {
		return true
	}
	// Inspect each query key, even if another part of the query is malformed.
	for _, part := range strings.Split(u.RawQuery, "&") {
		key, _, _ := strings.Cut(part, "=")
		key, e = url.QueryUnescape(key)
		if e != nil {
			continue
		}
		if credentialName(key) {
			return true
		}
	}
	return false
}

func credentialName(name string) bool {
	name = strings.ReplaceAll(strings.ToLower(name), "-", "_")
	return suspiciousName(name) || name == "key" || name == "auth" || name == "authorization"
}

func secretArgument(text string) bool {
	if strings.Contains(text, "${") || shellExpansion.MatchString(text) || strings.Contains(text, "op://") || secretURL(text) || credentialFlag(text) {
		return true
	}
	if strings.HasPrefix(strings.ToLower(text), "-e ") {
		return secretArgument(strings.TrimSpace(text[3:]))
	}
	if key, value, ok := strings.Cut(text, "="); ok {
		if suspiciousName(key) || secretArgument(value) {
			return true
		}
	}
	key, _, ok := strings.Cut(text, ":")
	return ok && protectedHeader(strings.TrimSpace(key))
}

func secretHeaderArgument(text string) bool {
	name, _, ok := strings.Cut(text, ":")
	return ok && protectedHeader(strings.TrimSpace(name))
}
