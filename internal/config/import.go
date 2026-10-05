package config

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dedene/mcparcel/internal/jsonutil"
)

func ImportMcporter(raw []byte, bindings map[string]CredentialBinding) (ImportReport, error) {
	if err := validateBindings(bindings); err != nil {
		return ImportReport{}, err
	}
	if len(raw) > maxConfigBytes {
		return ImportReport{}, fieldError("import", "configuration exceeds 2097152 bytes")
	}
	decoded, err := jsonutil.Decode(raw)
	if err != nil {
		return ImportReport{}, fieldError("import", "invalid JSON")
	}
	root, ok := decoded.(map[string]any)
	if !ok {
		return ImportReport{}, ErrConfig
	}
	for key := range root {
		if key != "mcpServers" && key != "imports" {
			return ImportReport{}, fieldError("import", "unknown field")
		}
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		return ImportReport{}, ErrConfig
	}
	external := false
	if value, exists := root["imports"]; exists {
		list, ok := value.([]any)
		if !ok {
			return ImportReport{}, ErrConfig
		}
		for _, v := range list {
			if _, ok := v.(string); !ok {
				return ImportReport{}, ErrConfig
			}
		}
		external = len(list) > 0
	}
	report := ImportReport{Entries: []ImportEntry{}, Definitions: Catalog{SchemaVersion: 1, Connections: map[string]Connection{}, CredentialProfiles: map[string]ProfileRequirement{}, Domains: map[string]Domain{}}, Aliases: map[string]string{}, Issues: []ImportIssue{}}
	definitionBytes := len(`{"schemaVersion":1,"connections":{}}`)
	if external {
		report.Issues = append(report.Issues, ImportIssue{Path: "imports", Code: "unsupported_imports"})
	}
	used := map[string]bool{}
	for _, id := range sortedKeys(servers) {
		row, c := convertImportEntry(id, servers[id], bindings, used)
		if external {
			row.Unresolved = append(row.Unresolved, ImportIssue{Path: "imports", Code: "unsupported_imports"})
		}
		if identifier.MatchString(id) {
			report.Aliases[id] = "local:" + id
		}
		row.Applicable = len(row.Unresolved) == 0
		if row.Applicable {
			cat := Catalog{SchemaVersion: 1, Connections: map[string]Connection{id: c}, CredentialProfiles: map[string]ProfileRequirement{}}
			if c.CredentialProfile != "" {
				cat.CredentialProfiles[c.CredentialProfile] = ProfileRequirement{}
			}
			data, e := json.Marshal(cat)
			if e == nil {
				cat, e = DecodeCatalog(data)
			}
			addedBytes := 0
			if e == nil {
				addedBytes = importDefinitionBytes(report.Definitions, id, cat.Connections[id])
				if definitionBytes+addedBytes > maxConfigBytes {
					e = ErrConfig
				}
			}
			if e != nil {
				row.Unresolved = append(row.Unresolved, ImportIssue{Path: "mcpServers." + id, Code: "invalid_definition"})
				row.Applicable = false
			} else {
				definitionBytes += addedBytes
				report.Definitions.Connections[id] = cat.Connections[id]
				for k, v := range cat.CredentialProfiles {
					report.Definitions.CredentialProfiles[k] = v
				}
			}
		}
		sortImportIssues(row.Unresolved)
		sortImportIssues(row.Warnings)
		report.Entries = append(report.Entries, row)
	}
	for _, name := range sortedKeys(bindings) {
		if !used[name] {
			report.Issues = append(report.Issues, ImportIssue{Path: "bindings." + name, Code: "unused_binding"})
		}
	}
	sortImportIssues(report.Issues)
	data, err := json.Marshal(report.Definitions)
	if err != nil {
		return ImportReport{}, ErrConfig
	}
	report.Definitions, err = DecodeCatalog(data)
	if err != nil {
		return ImportReport{}, ErrConfig
	}
	return report, nil
}

// Count the exact JSON growth without repeatedly serializing the whole catalog.
// Connections are already normalized; imported requirements have empty descriptions.
func importDefinitionBytes(catalog Catalog, id string, connection Connection) int {
	data, _ := json.Marshal(connection)
	added := len(id) + 3 + len(data)
	if len(catalog.Connections) > 0 {
		added++
	}
	profile := connection.CredentialProfile
	if _, exists := catalog.CredentialProfiles[profile]; profile != "" && !exists {
		added += len(profile) + 5
		if len(catalog.CredentialProfiles) == 0 {
			added += len(`,"credentialProfiles":{}`)
		} else {
			added++
		}
	}
	return added
}

func sortImportIssues(issues []ImportIssue) {
	slices.SortFunc(issues, func(a, b ImportIssue) int {
		if n := strings.Compare(a.Path, b.Path); n != 0 {
			return n
		}
		if n := strings.Compare(a.Code, b.Code); n != 0 {
			return n
		}
		return strings.Compare(a.Variable, b.Variable)
	})
}

var importFields = map[string]string{
	"command": "string", "baseUrl": "string", "args": "array", "env": "map", "headers": "map", "description": "string", "auth": "string", "clientName": "string", "oauthClientId": "string", "oauthClientSecret": "string", "oauthRedirectUrl": "string", "oauthScope": "string", "oauthTokenEndpointAuthMethod": "string",
}

func convertImportEntry(id string, raw any, bindings map[string]CredentialBinding, used map[string]bool) (ImportEntry, Connection) {
	path := "mcpServers." + id
	row := ImportEntry{ID: id, Alias: id, Fields: []string{}, Unresolved: []ImportIssue{}, Warnings: []ImportIssue{}, Selected: true}
	c := Connection{}
	block := func(field, code string) {
		row.Unresolved = append(row.Unresolved, ImportIssue{Path: path + field, Code: code})
	}
	warn := func(field, code string) {
		row.Warnings = append(row.Warnings, ImportIssue{Path: path + field, Code: code})
	}
	if identifier.MatchString(id) {
		row.CanonicalID = "local:" + id
	} else {
		block("", "invalid_id")
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		block("", "invalid_field")
		return row, c
	}
	row.Fields = sortedKeys(obj)
	valid := map[string]any{}
	for _, key := range row.Fields {
		value := obj[key]
		kind, known := importFields[key]
		if !known {
			block("."+key, "unknown_field")
			continue
		}
		good := false
		switch kind {
		case "string":
			_, good = value.(string)
		case "array":
			if a, ok := value.([]any); ok {
				good = true
				filtered := append([]any{}, a...)
				for i, v := range a {
					if _, ok := v.(string); !ok {
						block(fmt.Sprintf(".%s[%d]", key, i), "invalid_field")
						filtered[i] = ""
					}
				}
				value = filtered
			}
		case "map":
			if m, ok := value.(map[string]any); ok {
				good = true
				filtered := map[string]any{}
				for _, name := range sortedKeys(m) {
					if _, ok := m[name].(string); !ok {
						block("."+key+"."+name, "invalid_field")
					} else {
						filtered[name] = m[name]
					}
				}
				value = filtered
			}
		}
		if !good {
			block("."+key, "invalid_field")
		} else {
			valid[key] = value
		}
	}
	text := func(key string) string { s, _ := valid[key].(string); return s }
	_, command := obj["command"]
	_, http := obj["baseUrl"]
	if command == http {
		block("", "transport_conflict")
	}
	if command {
		row.Transport = "stdio"
		c.Transport.Stdio = &Stdio{Command: Literal(text("command")), Args: []Value{}, Env: map[string]Value{}}
		if _, exists := obj["headers"]; exists {
			block(".headers", "transport_conflict")
		}
	}
	if http {
		row.Transport = "http"
		c.Transport = Transport{HTTP: &HTTP{URL: Literal(text("baseUrl")), Headers: map[string]Value{}, Mode: "auto", AllowInsecureHTTP: "never"}}
		for _, key := range []string{"args", "env"} {
			if _, exists := obj[key]; exists {
				block("."+key, "transport_conflict")
			}
		}
	}
	for _, key := range []string{"command", "baseUrl"} {
		if _, exists := obj[key]; exists && text(key) == "" {
			block("."+key, "invalid_field")
		}
	}
	c.Description = text("description")
	_, auth := obj["auth"]
	if auth {
		row.OAuth = text("auth") == "oauth"
		if !row.OAuth {
			block(".auth", "invalid_field")
		}
		if !http {
			block(".auth", "transport_conflict")
		}
		c.Auth = &OAuth{Type: "oauth", ClientName: text("clientName"), RedirectURL: text("oauthRedirectUrl"), TokenEndpointAuthMethod: text("oauthTokenEndpointAuthMethod"), Scopes: []string{}}
		for _, scope := range strings.Split(text("oauthScope"), " ") {
			if scope != "" {
				c.Auth.Scopes = append(c.Auth.Scopes, scope)
			}
		}
		warn(".auth", "runtime_oauth_pending")
	} else {
		for _, key := range []string{"clientName", "oauthClientId", "oauthClientSecret", "oauthRedirectUrl", "oauthScope", "oauthTokenEndpointAuthMethod"} {
			if _, exists := obj[key]; exists {
				block("."+key, "auth_required_in_definition")
			}
		}
	}
	profiles := map[string]bool{}
	convert := func(s, field string, protected, header bool) Value {
		if secretURL(s) {
			block(field, "secret_in_args")
			return Value{}
		}
		for _, m := range sourceReference.FindAllStringSubmatch(s, -1) {
			used[m[1]] = true
		}
		// Only the supported ${NAME} form may survive as a reference.
		stripped := sourceReference.ReplaceAllString(s, "")
		if shellExpansion.MatchString(stripped) {
			block(field, "unsupported_expansion")
			return Value{}
		}
		v, p, i := importValue(s, path+field, bindings)
		if i != nil {
			row.Unresolved = append(row.Unresolved, *i)
			return Value{}
		}
		if p != "" {
			profiles[p] = true
		}
		if protected && v.Literal != nil {
			block(field, "potential_secret")
			return Value{}
		}
		if err := validateValue(v, path+field, header); err != nil {
			block(field, "invalid_field")
			return Value{}
		}
		return v
	}
	for _, key := range []string{"command", "baseUrl", "clientName", "oauthRedirectUrl", "oauthScope", "oauthTokenEndpointAuthMethod"} {
		if s, exists := valid[key].(string); exists {
			if strings.Contains(s, "${") || shellExpansion.MatchString(s) {
				block("."+key, "unsupported_expansion")
			}
			if secretURL(s) {
				block("."+key, "secret_in_args")
			}
			if strings.Contains(s, "op://") {
				block("."+key, "potential_secret")
			}
		}
	}
	if filepath.IsAbs(text("command")) {
		warn(".command", "local_value")
	}
	if args, ok := valid["args"].([]any); ok {
		for i, v := range args {
			a := v.(string)
			bad := secretArgument(a)
			lower := strings.ToLower(a)
			if i+1 < len(args) && (lower == "-e" && secretArgument(args[i+1].(string)) || (lower == "-h" || lower == "--header") && secretHeaderArgument(args[i+1].(string))) {
				bad = true
			}
			if prefix, value, found := strings.Cut(a, "="); found && (strings.EqualFold(prefix, "--header") || strings.EqualFold(prefix, "-H")) && secretHeaderArgument(value) {
				bad = true
			}
			// Report at the flag rather than the following credential value.
			previousFlag := i > 0 && (credentialFlag(args[i-1].(string)) || (strings.EqualFold(args[i-1].(string), "-e") && bad) || (strings.EqualFold(args[i-1].(string), "-H") || strings.EqualFold(args[i-1].(string), "--header")) && secretHeaderArgument(a))
			if bad && !previousFlag {
				block(fmt.Sprintf(".args[%d]", i), "secret_in_args")
			}
			if s := c.Transport.Stdio; s != nil {
				s.Args = append(s.Args, Literal(a))
			}
		}
	}
	for _, key := range []string{"env", "headers"} {
		values, ok := valid[key].(map[string]any)
		if !ok {
			continue
		}
		seen := map[string]bool{}
		for _, name := range sortedKeys(values) {
			field := "." + key + "." + name
			header := key == "headers"
			protected := suspiciousName(name)
			if header {
				protected = protectedHeader(name)
				lower := strings.ToLower(name)
				if !headerName.MatchString(name) || seen[lower] {
					block(field, "invalid_field")
				}
				seen[lower] = true
				if auth && lower == "authorization" {
					block(field, "transport_conflict")
				}
			} else if !envName.MatchString(name) || forbiddenExplicit(name) {
				block(field, "invalid_field")
			}
			v := convert(values[name].(string), field, protected, header)
			if header && c.Transport.HTTP != nil {
				c.Transport.HTTP.Headers[name] = v
			}
			if !header && c.Transport.Stdio != nil {
				c.Transport.Stdio.Env[name] = v
			}
			if !header && localImportEnv(name) {
				warn(field, "local_value")
			}
		}
	}
	for _, key := range []string{"oauthClientId", "oauthClientSecret"} {
		if s, exists := valid[key].(string); exists {
			v := convert(s, "."+key, key == "oauthClientSecret", false)
			if c.Auth != nil {
				if key == "oauthClientId" {
					c.Auth.ClientID = &v
				} else {
					c.Auth.ClientSecret = &v
				}
			}
		}
	}
	if h := c.Transport.HTTP; h != nil {
		u, e := parseHTTPURL(text("baseUrl"))
		if e != nil {
			block(".baseUrl", "invalid_field")
		} else {
			if u.Scheme == "http" {
				h.AllowInsecureHTTP = "explicit"
				if loopbackHost(u.Hostname()) {
					h.AllowInsecureHTTP = "loopback"
				}
				warn(".baseUrl", "insecure_http")
			}
			if strings.HasSuffix(u.Path, "/sse") {
				warn(".baseUrl", "transport_unverified")
			}
		}
		if id == "paper" || id == "home-assistant" {
			warn(".baseUrl", "local_value")
		}
	}
	if len(profiles) > 1 {
		block(".credentialProfile", "profile_conflict")
	} else {
		for p := range profiles {
			c.CredentialProfile = p
			row.CredentialProfile = p
		}
	}
	return row, c
}

func localImportEnv(name string) bool {
	switch name {
	case "RAILS_BLOCKS_CLI", "JAVA_HOME", "BLENDER_HOST", "BLENDER_PORT", "PROXMOX_HOST", "PROXMOX_PORT", "PROXMOX_USER", "PROXMOX_TOKEN_NAME", "PROXMOX_VERIFY_SSL":
		return true
	}
	return false
}
