package output

import (
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

func TestHumanInspectRetainsOriginAndBlockers(t *testing.T) {
	for _, tc := range []struct {
		id, origin string
		available  bool
	}{
		{"github:fixture-owner/fixture.repo#paper", "fixture-owner/fixture.repo", false},
		{"local:paper", "Personal", true},
	} {
		t.Run(tc.id, func(t *testing.T) {
			got := humanInspect(InspectData{Item: config.EffectiveConnection{ID: tc.id, Available: tc.available, Blockers: []string{"config_required", "review_required"}}})
			for _, want := range []string{"Source: " + tc.origin + "\n", `"blockers": [`, `"config_required"`, `"review_required"`} {
				if !strings.Contains(got, want) {
					t.Errorf("inspect missing %q: %s", want, got)
				}
			}
		})
	}
}
