package catalog

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

func TestDecodeWireRejectsCaseVariantConsumedFields(t *testing.T) {
	for _, dest := range []any{&repositoryResponse{}, &commitResponse{}, &treeResponse{}, &gitBlob{}} {
		typ := reflect.TypeOf(dest).Elem()
		value := wireFixture(typ).(map[string]any)
		var check func(map[string]any, reflect.Type, string)
		check = func(obj map[string]any, typ reflect.Type, path string) {
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				key := field.Tag.Get("json")
				t.Run(typ.Name()+path+"/"+key, func(t *testing.T) {
					obj[strings.ToUpper(key)] = obj[key]
					defer delete(obj, strings.ToUpper(key))
					raw, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					if err := decodeWire(raw, dest); !errors.Is(err, ErrContentInvalid) {
						t.Fatalf("accepted case-variant %s: %v", key, err)
					}
				})
				switch field.Type.Kind() {
				case reflect.Struct:
					check(obj[key].(map[string]any), field.Type, path+"/"+key)
				case reflect.Slice:
					check(obj[key].([]any)[0].(map[string]any), field.Type.Elem(), path+"/"+key)
				}
			}
		}
		check(value, typ, "")
	}
}

func wireFixture(typ reflect.Type) any {
	switch typ.Kind() {
	case reflect.Struct:
		obj := map[string]any{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			obj[f.Tag.Get("json")] = wireFixture(f.Type)
		}
		return obj
	case reflect.Slice:
		return []any{wireFixture(typ.Elem())}
	case reflect.Pointer:
		return wireFixture(typ.Elem())
	case reflect.String:
		return "fixture"
	case reflect.Bool:
		return false
	default:
		return 1
	}
}

func TestFetchRejectsTruncatedCaseOverride(t *testing.T) {
	r := newFetchRig()
	r.tree["truncated"] = true
	api := apiFunc(func(ctx context.Context, endpoint string) (APIResponse, error) {
		resp, err := r.Get(ctx, endpoint)
		if strings.Contains(endpoint, "/git/trees/") {
			resp.Body = append(resp.Body[:len(resp.Body)-1], []byte(`,"TRUNCATED":false}`)...)
		}
		return resp, err
	})
	_, err := (&GitHub{Public: api}).Fetch(t.Context(), initialSource())
	if !errors.Is(err, ErrContentInvalid) {
		t.Fatalf("accepted truncated override: %v", err)
	}
}

func TestFetchInvalidUTF8PreservesSnapshot(t *testing.T) {
	root, err := os.MkdirTemp("", "utf-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	env := map[string]string{"HOME": root + "/home", "XDG_CONFIG_HOME": root + "/config", "XDG_DATA_HOME": root + "/data", "XDG_CACHE_HOME": root + "/cache", "XDG_STATE_HOME": root + "/state", "MCPARCEL_RUNTIME_DIR": root + "/run"}
	paths, err := config.ResolvePaths(func(key string) string { return env[key] }, env["HOME"], root, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	rig := newFetchRig()
	github := &GitHub{Public: rig}
	saved, err := github.Fetch(t.Context(), initialSource())
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{Store: config.NewStore(paths), Fetcher: github}
	before, err := service.Register(t.Context(), saved, 0)
	if err != nil {
		t.Fatal(err)
	}
	snapshotPath := filepath.Join(paths.DataDir, "catalogs", saved.Source.ID, saved.Source.Commit+".json")
	savedBytes, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	savedInfo, err := os.Stat(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(strings.ReplaceAll(baseJSON, "fixture-retired", "fixture-\xff-retired"))
	hash := blobHash(raw)
	entry := rig.tree["tree"].([]any)[0].(map[string]any)
	entry["sha"], entry["size"] = hash, len(raw)
	rig.blob = map[string]any{"sha": hash, "size": len(raw), "encoding": "base64", "content": base64.StdEncoding.EncodeToString(raw)}
	rig.commit = commitB
	candidate, err := service.Fetcher.Fetch(t.Context(), saved.Source)
	if !errors.Is(err, ErrContentInvalid) || !reflect.DeepEqual(candidate, Snapshot{}) {
		t.Fatalf("malformed UTF-8 accepted: error=%v candidate=%+v", err, candidate)
	}
	after, err := service.Store.Read(t.Context())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("saved state changed: %v", err)
	}
	afterBytes, err := os.ReadFile(snapshotPath)
	if err != nil || !bytes.Equal(savedBytes, afterBytes) {
		t.Fatalf("snapshot changed: %v", err)
	}
	afterInfo, err := os.Stat(snapshotPath)
	if err != nil || !os.SameFile(savedInfo, afterInfo) || !savedInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatalf("snapshot rewritten: %v", err)
	}
}

// A catalog for a newer MCParcel can use fields this build rejects. Fetch
// still calls it invalid, but keeps its minVersion so add and sync can say an
// upgrade is needed instead.
func TestFetchKeepsMinVersionOfUndecodableCatalog(t *testing.T) {
	for _, c := range []struct{ raw, min string }{
		{`{"schemaVersion":1,"minVersion":"9.0.0","futureField":{"x":1},"connections":{}}`, "9.0.0"},
		{`{"schemaVersion":1,"futureField":{"x":1},"connections":{}}`, ""},
		{`{"schemaVersion":1,"minVersion":"9.0.0","connections":{"x":{"transport":{"type":"grpc"}}}}`, "9.0.0"},
	} {
		rig := newFetchRig()
		raw := []byte(c.raw)
		hash := blobHash(raw)
		entry := rig.tree["tree"].([]any)[0].(map[string]any)
		entry["sha"], entry["size"] = hash, len(raw)
		rig.blob = map[string]any{"sha": hash, "size": len(raw), "encoding": "base64", "content": base64.StdEncoding.EncodeToString(raw)}
		got, err := (&GitHub{Public: rig}).Fetch(t.Context(), initialSource())
		var invalid *InvalidCatalogError
		if !errors.Is(err, ErrContentInvalid) || !errors.As(err, &invalid) || invalid.MinVersion != c.min || !reflect.DeepEqual(got, Snapshot{}) {
			t.Fatalf("%s: %+v %v", c.raw, got, err)
		}
	}
}

// Presets and suggestions survive fetch, the saved snapshot and a read of the
// configuration, and resolve to canonical connection IDs.
func TestFetchKeepsJoinFields(t *testing.T) {
	root, err := os.MkdirTemp("", "join-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	env := map[string]string{"HOME": root + "/home", "XDG_CONFIG_HOME": root + "/config", "XDG_DATA_HOME": root + "/data", "XDG_CACHE_HOME": root + "/cache", "XDG_STATE_HOME": root + "/state", "MCPARCEL_RUNTIME_DIR": root + "/run"}
	paths, err := config.ResolvePaths(func(key string) string { return env[key] }, env["HOME"], root, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	rig := newFetchRig()
	raw := []byte(`{"schemaVersion":1,"minVersion":"0.2.0","credentialProfiles":{"team":{"suggest":{"mode":"desktop","account":"fixture.1password.com"}}},` +
		`"presets":[{"id":"writer","label":"Writer","connections":["paper"]}],"connections":{"paper":{"credentialProfile":"team","transport":{"type":"http","url":"https://paper.example.invalid/mcp"}}}}`)
	hash := blobHash(raw)
	entry := rig.tree["tree"].([]any)[0].(map[string]any)
	entry["sha"], entry["size"] = hash, len(raw)
	rig.blob = map[string]any{"sha": hash, "size": len(raw), "encoding": "base64", "content": base64.StdEncoding.EncodeToString(raw)}
	github := &GitHub{Public: rig}
	snapshot, err := github.Fetch(t.Context(), initialSource())
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{Store: config.NewStore(paths), Fetcher: github}
	if _, err = service.Register(t.Context(), snapshot, 0); err != nil {
		t.Fatal(err)
	}
	state, err := config.ReadState(t.Context(), paths)
	if err != nil {
		t.Fatal(err)
	}
	effective, err := config.Resolve(state)
	if err != nil {
		t.Fatal(err)
	}
	offer := effective.Catalogs[snapshot.Source.ID]
	if len(offer.Presets) != 1 || !reflect.DeepEqual(offer.Presets[0].Connections, []string{"github:fixture-owner/fixture.repo#paper"}) ||
		len(offer.Requirements) != 1 || offer.Requirements[0].Suggest == nil || offer.Requirements[0].Suggest.Account != "fixture.1password.com" {
		t.Fatalf("%+v", offer)
	}
}
