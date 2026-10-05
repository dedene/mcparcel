package cmd

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/jsonutil"
)

type LocalCmd struct {
	Add    LocalAddCmd    `cmd:"" json:"-" help:"Add a personal connection definition offline."`
	Update LocalUpdateCmd `cmd:"" json:"-" help:"Update a personal connection definition offline."`
	Remove LocalRemoveCmd `cmd:"" json:"-" help:"Remove a personal connection definition offline."`
}
type LocalAddCmd struct {
	File string `required:"" name:"file" json:"-"`
}
type LocalUpdateCmd struct {
	ID   string `arg:"" required:"" json:"-"`
	File string `required:"" name:"file" json:"-"`
}
type LocalRemoveCmd struct {
	ID string `arg:"" required:"" json:"-"`
}

func localID(name string) (string, error) {
	id := strings.TrimPrefix(name, "local:")
	if _, err := config.CanonicalLocal(id); err != nil {
		return "", config.ErrConfig
	}
	return id, nil
}

func decodeLocalDefinition(raw []byte, personal config.Catalog) (string, config.Catalog, error) {
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

func (c *LocalAddCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	raw, err := readCommandFile(ctx, c.File)
	if err != nil {
		return err
	}
	store, state, err := metadataStore(ctx)
	if err != nil {
		return err
	}
	next, err := store.Update(ctx, state.Selections.Revision, func(draft *config.State) error {
		id, personal, err := decodeLocalDefinition(raw, draft.Personal)
		if err != nil {
			return err
		}
		if existing, ok := draft.Personal.Connections[id]; ok {
			if !reflect.DeepEqual(existing, personal.Connections[id]) {
				return config.ErrConfig
			}
			return nil
		}
		draft.Personal = personal
		canonical := "local:" + id
		selection := draft.Selections.Connections[canonical]
		if selection.Enabled {
			selection.ReviewRequired = true
		}
		selection.Enabled = false
		draft.Selections.Connections[canonical] = selection
		return validateLocalBindings(*draft)
	})
	if err != nil {
		return err
	}
	return writeMutation(s, opts, next)
}

func (c *LocalUpdateCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	id, err := localID(c.ID)
	if err != nil {
		return err
	}
	raw, err := readCommandFile(ctx, c.File)
	if err != nil {
		return err
	}
	store, state, err := metadataStore(ctx)
	if err != nil {
		return err
	}
	next, err := store.Update(ctx, state.Selections.Revision, func(draft *config.State) error {
		fileID, personal, err := decodeLocalDefinition(raw, draft.Personal)
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
		return validateLocalBindings(*draft)
	})
	if err != nil {
		return err
	}
	return writeMutation(s, opts, next)
}

func (c *LocalRemoveCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	id, err := localID(c.ID)
	if err != nil {
		return err
	}
	store, state, err := metadataStore(ctx)
	if err != nil {
		return err
	}
	next, err := store.Update(ctx, state.Selections.Revision, func(draft *config.State) error {
		if _, exists := draft.Personal.Connections[id]; !exists {
			return config.ErrNotFound
		}
		delete(draft.Personal.Connections, id)
		return nil
	})
	if err != nil {
		return err
	}
	return writeMutation(s, opts, next)
}

func validateLocalBindings(state config.State) error {
	if err := config.ValidateState(state); err != nil {
		return config.ErrConfigRequired
	}
	if _, err := config.Resolve(state); err != nil {
		return config.ErrConfigRequired
	}
	return nil
}
