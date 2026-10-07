package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	"github.com/dedene/mcparcel/internal/testutil"
)

const (
	idPaper   = "github:acmeco/mcp-catalog#paper"
	idFigma   = "github:acmeco/mcp-catalog#figma"
	idNotes   = "github:acmeco/mcp-catalog#notes"
	idReview  = "github:acmeco/mcp-catalog#review"
	idGone    = "local:gone"
	idExcali  = "local:excalidraw"
	fixSource = "github-42"
)

func fixtureSource() config.Source {
	return config.Source{ID: fixSource, RepositoryID: 42, Owner: "acmeco", Repo: "mcp-catalog", Path: "mcparcel.json", Ref: "main", Commit: strings.Repeat("a", 40)}
}

func stdio(command string, args ...string) config.Transport {
	values := make([]config.Value, 0, len(args))
	for _, arg := range args {
		values = append(values, config.Literal(arg))
	}
	return config.Transport{Stdio: &config.Stdio{Command: config.Literal(command), Args: values}}
}

// seedFixture writes one catalog and personal connections covering every row
// state setup renders: two domains, unavailable, review, missing configuration,
// an HTTP URL with path and query, and literal arguments holding a token marker.
func seedFixture(t *testing.T, store *config.Store) {
	t.Helper()
	ctx := context.Background()
	state, err := store.Update(ctx, 0, func(s *config.State) error {
		s.Local.Sources = []config.Source{fixtureSource()}
		s.Local.CredentialProfiles["work"] = config.Profile{Mode: "desktop", Account: "Fixture account"}
		s.Catalogs[fixSource] = config.Catalog{
			SchemaVersion:      1,
			Domains:            map[string]config.Domain{"design": {Label: "Design"}, "development": {Label: "Development"}, "research": {Label: "Research"}},
			CredentialProfiles: map[string]config.ProfileRequirement{"team": {}},
			Connections: map[string]config.Connection{
				"paper": {Label: "Paper", Description: "Connect to the Paper app on this device.", Domains: []string{"design", "development"}, Transport: stdio("paper-fixture")},
				"figma": {Label: "Figma", Description: "Remote design files.", Domains: []string{"design"}, Transport: config.Transport{HTTP: &config.HTTP{URL: config.Literal("https://mcp.figma.example/private/path?team=QUERY-MARKER")}}},
				"notes": {
					Label: "Notes", Description: "Team notes.", Domains: []string{"research"}, CredentialProfile: "team",
					Inputs:    map[string]config.Input{"workspace": {Kind: "string", Description: "Workspace"}},
					Transport: stdio("notes-fixture", "--token", "SECRET-MARKER"),
				},
				"review": {Label: "Reviewer", Description: "Changed upstream.", Domains: []string{"development"}, Transport: stdio("review-fixture")},
			},
		}
		s.Personal.Connections["excalidraw"] = config.Connection{Label: "Excalidraw", Transport: stdio("excalidraw-fixture")}
		s.Personal.Connections["gone"] = config.Connection{Label: "Gone", Transport: stdio("gone-fixture")}
		s.Selections.Connections[idExcali] = config.Selection{Enabled: true}
		s.Selections.Connections[idGone] = config.Selection{Enabled: true}
		s.Selections.Connections[idReview] = config.Selection{Enabled: true, ReviewRequired: true}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Update(ctx, state.Selections.Revision, func(s *config.State) error {
		delete(s.Personal.Connections, "gone")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func fixtureStore(t *testing.T, seed func(*testing.T, *config.Store)) (*config.Store, config.Paths, config.State) {
	t.Helper()
	paths, _ := testutil.IsolatedPaths(t)
	store := config.NewStore(paths)
	if seed != nil {
		seed(t, store)
	}
	state, err := store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return store, paths, state
}

// describeFixture maps config sentinels like cmd.safeFailure does for the
// codes setup renders.
func describeFixture(err error) *output.Error {
	var safe *output.Error
	if errors.As(err, &safe) {
		return safe
	}
	code := "internal_error"
	switch {
	case errors.Is(err, config.ErrConfigConflict):
		code = "config_conflict"
	case errors.Is(err, config.ErrConfigWrite):
		code = "config_write_failed"
	case errors.Is(err, config.ErrConfigRequired):
		code = "config_required"
	case errors.Is(err, config.ErrNotFound):
		code = "connection_unavailable"
	case errors.Is(err, config.ErrConfig):
		code = "invalid_config"
	}
	return output.NewError(code, nil)
}

func mustUpdate(t *testing.T, store *config.Store, mutate func(*config.State)) config.State {
	t.Helper()
	state, err := store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	next, err := store.Update(context.Background(), state.Selections.Revision, func(s *config.State) error { mutate(s); return nil })
	if err != nil {
		t.Fatal(err)
	}
	return next
}

// seedSecrets adds personal connections holding markers setup must never
// render: literal argument, env and header values, a URL path and query, and
// a url input value. Spare is a disabled personal stdio connection.
func seedSecrets(t *testing.T, store *config.Store) {
	t.Helper()
	seedFixture(t, store)
	mustUpdate(t, store, func(s *config.State) {
		cwd := config.Literal("/tmp/fixture-cwd")
		s.Personal.Connections["vault"] = config.Connection{
			Label:  "Vault",
			Inputs: map[string]config.Input{"endpoint": {Kind: "url", Description: "Endpoint"}},
			Transport: config.Transport{Stdio: &config.Stdio{
				Command: config.Literal("vault-fixture"),
				Args:    []config.Value{config.Literal("--token"), config.Literal("SECRET-MARKER")},
				Env:     map[string]config.Value{"API_KEY": config.Literal("ENV-MARKER")},
				Cwd:     &cwd,
			}},
		}
		s.Personal.Connections["hook"] = config.Connection{Label: "Hook", Transport: config.Transport{HTTP: &config.HTTP{
			URL:     config.Literal("https://hook.example/private/path?q=QUERY-MARKER"),
			Headers: map[string]config.Value{"X-Key": config.Literal("HEADER-MARKER")},
		}}}
		s.Personal.Connections["spare"] = config.Connection{Label: "Spare", Transport: stdio("spare-fixture")}
		s.Selections.Connections["local:vault"] = config.Selection{Inputs: map[string]string{"endpoint": "https://in.example/private/path?q=QUERY-MARKER"}}
	})
}

// secretMarkers must never appear on any setup screen.
var secretMarkers = []string{"SECRET-MARKER", "ENV-MARKER", "HEADER-MARKER", "QUERY-MARKER", "private/path", "Fixture account", "fixture-cwd"}
