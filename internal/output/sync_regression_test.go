package output

import "testing"

func TestHumanSyncNoSourcesGuidance(t *testing.T) {
	for _, apply := range []bool{false, true} {
		got := humanSync(SyncData{Apply: apply, Results: []SourceSyncResult{}})
		if got != "No catalogs are registered. Run 'mcparcel add <owner/repo>' to register one.\n" {
			t.Errorf("apply=%v output=%q", apply, got)
		}
	}
}
