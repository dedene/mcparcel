package edit

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/jsonutil"
)

// LocalID returns the personal connection ID, accepting a "local:" prefix.
func LocalID(name string) (string, error) {
	id := strings.TrimPrefix(name, "local:")
	if _, err := config.CanonicalLocal(id); err != nil {
		return "", config.ErrConfig
	}
	return id, nil
}

// DecodeLocalDefinition decodes a flattened {id, ...} personal definition and
// returns the personal catalog with that connection added or replaced.
func DecodeLocalDefinition(raw []byte, personal config.Catalog) (string, config.Catalog, error) {
	if len(raw) > 2*1024*1024 {
		return "", config.Catalog{}, config.ErrConfig
	}
	value, err := jsonutil.Decode(raw)
	if err != nil {
		return "", config.Catalog{}, config.ErrConfig
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return "", config.Catalog{}, config.ErrConfig
	}
	id, ok := obj["id"].(string)
	if !ok {
		return "", config.Catalog{}, config.ErrConfig
	}
	if _, err := config.CanonicalLocal(id); err != nil {
		return "", config.Catalog{}, config.ErrConfig
	}
	delete(obj, "id")
	original, err := json.Marshal(personal)
	if err != nil {
		return "", config.Catalog{}, config.ErrConfig
	}
	normalized, err := config.DecodeCatalog(original)
	if err != nil {
		return "", config.Catalog{}, err
	}
	// The flattened format declares assignments; missing root declarations are inert metadata.
	if domains, ok := obj["domains"].([]any); ok {
		for _, domain := range domains {
			if name, ok := domain.(string); ok {
				if _, exists := normalized.Domains[name]; !exists {
					normalized.Domains[name] = config.Domain{Label: name}
				}
			}
		}
	}
	if requirement, ok := obj["credentialProfile"].(string); ok && requirement != "" {
		if _, exists := normalized.CredentialProfiles[requirement]; !exists {
			normalized.CredentialProfiles[requirement] = config.ProfileRequirement{}
		}
	}
	rawConnection, err := json.Marshal(obj)
	if err != nil {
		return "", config.Catalog{}, config.ErrConfig
	}
	connections := make(map[string]json.RawMessage, len(normalized.Connections)+1)
	for name, connection := range normalized.Connections {
		encoded, err := json.Marshal(connection)
		if err != nil {
			return "", config.Catalog{}, config.ErrConfig
		}
		connections[name] = encoded
	}
	connections[id] = rawConnection
	wrapped, err := json.Marshal(map[string]any{"schemaVersion": normalized.SchemaVersion, "name": normalized.Name, "domains": normalized.Domains, "credentialProfiles": normalized.CredentialProfiles, "connections": connections})
	if err != nil {
		return "", config.Catalog{}, config.ErrConfig
	}
	result, err := config.DecodeCatalog(wrapped)
	if err != nil {
		return "", config.Catalog{}, err
	}
	return id, result, nil
}

// StageLocalAdd adds a personal definition, stored disabled. An equal existing
// definition is a no-op; a different one is ErrConfig.
func StageLocalAdd(draft *config.State, raw []byte) (string, error) {
	id, personal, err := DecodeLocalDefinition(raw, draft.Personal)
	if err != nil {
		return "", err
	}
	if existing, ok := draft.Personal.Connections[id]; ok {
		if !reflect.DeepEqual(existing, personal.Connections[id]) {
			return "", config.ErrConfig
		}
		return id, nil
	}
	draft.Personal = personal
	canonical := "local:" + id
	selection := draft.Selections.Connections[canonical]
	if selection.Enabled {
		selection.ReviewRequired = true
	}
	selection.Enabled = false
	draft.Selections.Connections[canonical] = selection
	return id, ValidateLocalBindings(*draft)
}

// StageLocalUpdate replaces an existing personal definition with the same ID.
// An enabled connection whose execution changed needs review.
func StageLocalUpdate(draft *config.State, id string, raw []byte) error {
	fileID, personal, err := DecodeLocalDefinition(raw, draft.Personal)
	if err != nil {
		return err
	}
	if fileID != id {
		return config.ErrConfig
	}
	before, exists := draft.Personal.Connections[id]
	if !exists {
		return config.ErrNotFound
	}
	canonical := "local:" + id
	selection := draft.Selections.Connections[canonical]
	if selection.Enabled && config.ExecutionChanged(before, personal.Connections[id]) {
		selection.ReviewRequired = true
		draft.Selections.Connections[canonical] = selection
	}
	draft.Personal = personal
	return ValidateLocalBindings(*draft)
}

// ValidateLocalBindings reports ErrConfigRequired when the state no longer
// validates or resolves.
func ValidateLocalBindings(state config.State) error {
	if err := config.ValidateState(state); err != nil {
		return config.ErrConfigRequired
	}
	if _, err := config.Resolve(state); err != nil {
		return config.ErrConfigRequired
	}
	return nil
}
