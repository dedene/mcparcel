package config

import (
	"encoding/json"
	"strings"
)

func DecodeSelections(data []byte) (Selections, error) {
	var s Selections
	if err := decodeStrict(data, &s, "selections"); err != nil {
		return Selections{}, err
	}
	if s.SchemaVersion != 1 {
		return Selections{}, fieldError("schemaVersion", "unsupported version")
	}
	if s.Revision > MaxRevision {
		return Selections{}, fieldError("revision", "interoperable revision required")
	}
	if s.Connections == nil {
		return Selections{}, fieldError("connections", "object required")
	}
	for _, id := range sortedKeys(s.Connections) {
		selection := s.Connections[id]
		path := "connections." + id
		if ValidateCanonicalID(id) != nil {
			return Selections{}, fieldError("connections", "canonical connection ID required")
		}
		if selection.CredentialProfile != "" && !identifier.MatchString(selection.CredentialProfile) {
			return Selections{}, fieldError(path+".credentialProfile", "invalid identifier")
		}
		for _, name := range sortedKeys(selection.Inputs) {
			if !identifier.MatchString(name) {
				return Selections{}, fieldError(path+".inputs", "invalid identifier")
			}
			if strings.ContainsRune(selection.Inputs[name], 0) {
				return Selections{}, fieldError(path+".inputs."+name, "invalid value characters")
			}
		}
		if err := validateToolNames(selection.DisabledTools, path+".disabledTools"); err != nil {
			return Selections{}, err
		}
	}
	return s, nil
}

func ValidateState(state State) error {
	localData, err := json.Marshal(state.Local)
	if err != nil {
		return fieldError("local", "invalid configuration")
	}
	local, err := DecodeLocal(localData)
	if err != nil {
		return err
	}
	personalData, err := json.Marshal(state.Personal)
	if err != nil {
		return fieldError("personal", "invalid configuration")
	}
	personal, err := DecodeCatalog(personalData)
	if err != nil {
		return err
	}
	selectionsData, err := json.Marshal(state.Selections)
	if err != nil {
		return fieldError("selections", "invalid configuration")
	}
	selections, err := DecodeSelections(selectionsData)
	if err != nil {
		return err
	}
	catalogs := make(map[string]Catalog, len(state.Catalogs))
	for _, id := range sortedKeys(state.Catalogs) {
		data, marshalErr := json.Marshal(state.Catalogs[id])
		if marshalErr != nil {
			return fieldError("catalogs."+id, "invalid configuration")
		}
		catalog, decodeErr := DecodeCatalog(data)
		if decodeErr != nil {
			return decodeErr
		}
		catalogs[id] = catalog
	}
	present := make(map[string]Connection)
	for _, id := range sortedKeys(personal.Connections) {
		present["local:"+id] = personal.Connections[id]
	}
	for _, source := range local.Sources {
		catalog, ok := catalogs[source.ID]
		if !ok {
			return fieldError("catalogs", "source catalog required")
		}
		for _, id := range sortedKeys(catalog.Connections) {
			present["github:"+source.Owner+"/"+source.Repo+"#"+id] = catalog.Connections[id]
		}
		delete(catalogs, source.ID)
	}
	if len(catalogs) != 0 {
		return fieldError("catalogs", "unregistered source catalog")
	}
	for _, id := range sortedKeys(selections.Connections) {
		selection := selections.Connections[id]
		definition, exists := present[id]
		if !exists {
			continue
		}
		for _, input := range sortedKeys(selection.Inputs) {
			if _, declared := definition.Inputs[input]; !declared {
				return fieldError("selections.connections."+id+".inputs", "undeclared input")
			}
		}
		if selection.CredentialProfile != "" && definition.CredentialProfile == "" {
			return fieldError("selections.connections."+id+".credentialProfile", "connection has no credential requirement")
		}
	}
	return nil
}
