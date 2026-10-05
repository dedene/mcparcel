package output

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/catalog"
	"github.com/dedene/mcparcel/internal/config"
)

func TestMetadataHumanEscapes(t *testing.T) {
	row := config.EffectiveConnection{ID: "local:paper", Available: true, Enabled: true, Definition: &config.Connection{Label: "Label\x1b\nInjected"}}
	data := MetadataData{Revision: 7, Items: []config.EffectiveConnection{row}}
	var b bytes.Buffer
	if err := WriteHuman(&b, data); err != nil {
		t.Fatal(err)
	}
	want := "Revision: 7\nlocal:paper\t\"Label\\x1b\\nInjected\"\tReady\n"
	if b.String() != want {
		t.Fatalf("%q", b.String())
	}
	for _, v := range []any{&data, InspectData{Item: row}, &InspectData{Item: row}} {
		b.Reset()
		if err := WriteHuman(&b, v); err != nil || strings.ContainsAny(b.String(), "\x1b") {
			t.Fatal(b.String(), err)
		}
	}
	if DisplayMetadata("café") != `"caf\u00e9"` || DisplayMetadata("Plain") != "Plain" || DisplayMetadata("\x7f") != `"\x7f"` {
		t.Fatal("escaping")
	}
	for _, tc := range []struct {
		row   config.EffectiveConnection
		state string
	}{{config.EffectiveConnection{}, "Unavailable"}, {config.EffectiveConnection{Available: true}, "Disabled"}, {config.EffectiveConnection{Available: true, Enabled: true, ReviewRequired: true}, "Review required"}, {config.EffectiveConnection{Available: true, Enabled: true, Blockers: []string{"config_required"}}, "Configuration required"}} {
		if !strings.Contains(humanMetadata(MetadataData{Items: []config.EffectiveConnection{tc.row}}), "\t"+tc.state+"\n") {
			t.Fatal(tc)
		}
	}
}

func TestCatalogAllCommandsJSONDTOContracts(t *testing.T) {
	for _, tc := range []struct {
		name string
		data any
		want string
	}{
		{"mutation", SourceMutationData{Revision: 7, Source: config.Source{ID: "github-42", RepositoryID: 42, Owner: "fixture-owner", Repo: "fixture.repo", Path: "mcparcel.json", Ref: "main", Commit: strings.Repeat("a", 40)}, Changed: true}, `{"schemaVersion":1,"ok":true,"data":{"revision":7,"source":{"id":"github-42","repositoryId":42,"owner":"fixture-owner","repo":"fixture.repo","path":"mcparcel.json","ref":"main","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","pinned":false},"changed":true},"error":null}`},
		{"sync", SyncData{Apply: true, Results: []SourceSyncResult{{Repository: "fixture-owner/fixture.repo", SourceID: "github-42", Applied: true, Accepted: []string{"github:fixture-owner/fixture.repo#paper"}, Revision: 8}}}, `{"schemaVersion":1,"ok":true,"data":{"apply":true,"results":[{"repository":"fixture-owner/fixture.repo","sourceId":"github-42","plan":null,"applied":true,"accepted":["github:fixture-owner/fixture.repo#paper"],"revision":8,"error":null}]},"error":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			if err := WriteJSON(&b, tc.data, nil); err != nil {
				t.Fatal(err)
			}
			if b.String() != tc.want+"\n" {
				t.Fatalf("exact DTO keys/values changed: %s", b.String())
			}
		})
	}
}

func TestCatalogPrivacyHumanSyncEscapes(t *testing.T) {
	unsafe := "remote\x1b]0;injected\x07\nspoof"
	data := SyncData{Results: []SourceSyncResult{{Repository: unsafe, Plan: &catalog.UpdatePlan{CandidateCommit: unsafe, Added: []string{unsafe}, Removed: []string{unsafe}, Changed: []catalog.ConnectionChange{{ID: unsafe, Fields: []string{unsafe}, ExecutionOrAuth: true}}}, Accepted: []string{unsafe}}}}
	var b bytes.Buffer
	if err := WriteHuman(&b, data); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(b.String(), "\x1b\x07") || strings.Contains(b.String(), "\nspoof") || strings.Count(b.String(), DisplayMetadata(unsafe)) != 7 {
		t.Fatalf("unsafe or missing rendered metadata: %q", b.String())
	}
}
