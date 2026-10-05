package catalog

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/config"
)

const (
	commitA  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	commitB  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	baseJSON = `{"schemaVersion":1,"name":"Fixture team","domains":{"docs":{"label":"Documents"}},"connections":{"paper":{"description":"Original","transport":{"type":"http","url":"https://paper.example.invalid/mcp"}},"retired":{"transport":{"type":"stdio","command":"fixture-retired"}}}}`
)

var treeSHA = singleEntryTreeHash("mcparcel.json", "100644", blobHash([]byte(baseJSON)))

func singleEntryTreeHash(path, mode, sha string) string {
	object, _ := hex.DecodeString(sha)
	raw := append([]byte(strings.TrimLeft(mode, "0")+" "+path+"\x00"), object...)
	h := sha1.New()
	_, _ = fmt.Fprintf(h, "tree %d%c", len(raw), 0)
	_, _ = h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

type apiFunc func(context.Context, string) (APIResponse, error)

func (f apiFunc) Get(ctx context.Context, s string) (APIResponse, error) { return f(ctx, s) }
func response(v any) APIResponse {
	b, _ := json.Marshal(v)
	return APIResponse{Status: 200, Header: http.Header{}, Body: b}
}

func blobHash(b []byte) string {
	h := sha1.New()
	_, _ = fmt.Fprintf(h, "blob %d%c", len(b), 0)
	_, _ = h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

type fetchRig struct {
	calls    []string
	metadata map[string]any
	commit   string
	tree     map[string]any
	blob     map[string]any
	raw      []byte
}

func newFetchRig() *fetchRig {
	raw := []byte(baseJSON)
	hash := blobHash(raw)
	return &fetchRig{
		raw: raw, metadata: map[string]any{"id": 42, "full_name": "fixture-owner/fixture.repo", "default_branch": "main", "private": false}, commit: commitA,
		tree: map[string]any{"sha": treeSHA, "truncated": false, "tree": []any{map[string]any{"path": "mcparcel.json", "mode": "100644", "type": "blob", "sha": hash, "size": len(raw)}}},
		blob: map[string]any{"sha": hash, "size": len(raw), "encoding": "base64", "content": base64.StdEncoding.EncodeToString(raw)},
	}
}

func (r *fetchRig) Get(_ context.Context, e string) (APIResponse, error) {
	r.calls = append(r.calls, e)
	switch {
	case e == "/repos/fixture-owner/fixture.repo":
		return response(r.metadata), nil
	case strings.Contains(e, "/commits/"):
		return response(map[string]any{"sha": r.commit, "commit": map[string]any{"tree": map[string]any{"sha": treeSHA}}}), nil
	case strings.Contains(e, "/git/trees/"):
		return response(r.tree), nil
	case strings.Contains(e, "/git/blobs/"):
		return response(r.blob), nil
	default:
		return APIResponse{}, errors.New("unexpected endpoint")
	}
}

func initialSource() config.Source {
	return config.Source{Owner: "fixture-owner", Repo: "fixture.repo", Path: "mcparcel.json"}
}

func existingSource() config.Source {
	s := initialSource()
	s.ID = "github-42"
	s.RepositoryID = 42
	s.Ref = "main"
	s.Commit = commitA
	return s
}

func TestFetchDefaultAndPinnedRef(t *testing.T) {
	r := newFetchRig()
	g := GitHub{Public: r}
	got, err := g.Fetch(t.Context(), initialSource())
	if err != nil || got.Source.Ref != "main" || got.Source.Pinned || got.Source.Commit != commitA {
		t.Fatalf("%+v %v", got.Source, err)
	}
	s := initialSource()
	s.Ref = strings.ToUpper(commitA)
	r.commit = commitB // main has moved; the explicit immutable ref still resolves A.
	g.Public = apiFunc(func(ctx context.Context, endpoint string) (APIResponse, error) {
		resp, err := r.Get(ctx, endpoint)
		if strings.HasSuffix(endpoint, "/commits/"+commitA) {
			return response(map[string]any{"sha": commitA, "commit": map[string]any{"tree": map[string]any{"sha": treeSHA}}}), nil
		}
		return resp, err
	})
	got, err = g.Fetch(t.Context(), s)
	if err != nil || got.Source.Ref != commitA || !got.Source.Pinned || got.Source.Commit != commitA {
		t.Fatalf("%+v %v", got.Source, err)
	}
	s = got.Source
	s.Ref = "main"
	g.Public = r
	if _, err = g.Fetch(t.Context(), s); !errors.Is(err, ErrContentInvalid) {
		t.Fatal(err)
	}
	if !strings.HasSuffix(r.calls[len(r.calls)-1], "/commits/"+commitA) {
		t.Fatal(r.calls)
	}
}

func TestFetchBranchTag(t *testing.T) {
	for _, ref := range []string{"feature/team", "release-v1"} {
		t.Run(ref, func(t *testing.T) {
			r := newFetchRig()
			s := initialSource()
			s.Ref = ref
			got, err := (&GitHub{Public: r}).Fetch(t.Context(), s)
			if err != nil || got.Source.Pinned || got.Source.Ref != ref {
				t.Fatal(got, err)
			}
			escaped := strings.ReplaceAll(ref, "/", "%2F")
			want := []string{"/repos/fixture-owner/fixture.repo", "/repos/fixture-owner/fixture.repo/commits/" + escaped, "/repos/fixture-owner/fixture.repo/git/trees/" + treeSHA, "/repos/fixture-owner/fixture.repo/git/blobs/" + r.blob["sha"].(string)}
			if !reflect.DeepEqual(r.calls, want) {
				t.Fatal(r.calls)
			}
		})
	}
}

func TestSourceRenameOrReuse(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   int
		full string
		want error
	}{{"rename", 42, "fixture-owner/new-name", ErrSourceRenamed}, {"reuse", 43, "fixture-owner/fixture.repo", ErrRepositoryReused}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newFetchRig()
			r.metadata["id"] = tc.id
			r.metadata["full_name"] = tc.full
			_, err := (&GitHub{Public: r}).Fetch(t.Context(), existingSource())
			if !errors.Is(err, tc.want) || len(r.calls) != 1 {
				t.Fatal(err, r.calls)
			}
		})
	}
}

func TestFetchPrivateFallback(t *testing.T) {
	for _, private := range []bool{false, true} {
		t.Run(fmt.Sprint(private), func(t *testing.T) {
			count := 0
			r := newFetchRig()
			pub := apiFunc(func(context.Context, string) (APIResponse, error) {
				count++
				if private {
					m := newFetchRig().metadata
					m["private"] = true
					return response(m), nil
				}
				return APIResponse{Status: 404}, nil
			})
			_, err := (&GitHub{Public: pub, Private: r}).Fetch(t.Context(), initialSource())
			if err != nil || count != 1 || len(r.calls) != 4 {
				t.Fatal(err, count, r.calls)
			}
		})
	}
}

func TestFetchFailures(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		h             http.Header
		failure, want error
	}{
		{"401", 401, nil, nil, ErrUnauthorized}, {"404", 404, nil, nil, ErrRepositoryMissing}, {"429", 429, nil, nil, ErrRateLimited}, {"403", 403, http.Header{"X-Ratelimit-Remaining": []string{"0"}}, nil, ErrRateLimited}, {"network", 0, nil, ErrOffline, ErrOffline}, {"gh", 404, nil, ErrGHRequired, ErrGHRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pub := apiFunc(func(context.Context, string) (APIResponse, error) {
				if tc.name == "network" {
					return APIResponse{}, tc.failure
				}
				return APIResponse{Status: tc.status, Header: tc.h}, nil
			})
			priv := apiFunc(func(context.Context, string) (APIResponse, error) {
				return APIResponse{Status: tc.status, Header: tc.h}, tc.failure
			})
			_, err := (&GitHub{Public: pub, Private: priv}).Fetch(t.Context(), initialSource())
			if !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestRejectCatalogPath(t *testing.T) {
	for _, path := range []string{"/x", "../x", "a/../x", "a//x", "a/./x", "a\\b", "a%2fb", "a\nb", strings.Repeat("a/", 32) + "a"} {
		t.Run(fmt.Sprintf("%q", path), func(t *testing.T) {
			r := newFetchRig()
			s := initialSource()
			s.Path = path
			_, err := (&GitHub{Public: r}).Fetch(t.Context(), s)
			if !errors.Is(err, ErrContentInvalid) || len(r.calls) != 0 {
				t.Fatal(err, r.calls)
			}
		})
	}
}

func TestRejectNonRegularCatalog(t *testing.T) {
	for _, kind := range []string{"symlink-final", "symlink-ancestor", "submodule", "directory", "truncated", "duplicate", "missing-sha"} {
		t.Run(kind, func(t *testing.T) {
			r := newFetchRig()
			s := initialSource()
			entry := r.tree["tree"].([]any)[0].(map[string]any)
			switch kind {
			case "symlink-final":
				entry["mode"] = "120000"
			case "symlink-ancestor":
				s.Path = "mcparcel.json/x"
				entry["mode"] = "120000"
			case "submodule":
				entry["mode"] = "160000"
				entry["type"] = "commit"
			case "directory":
				entry["mode"] = "040000"
				entry["type"] = "tree"
			case "truncated":
				r.tree["truncated"] = true
			case "duplicate":
				r.tree["tree"] = []any{entry, entry}
			case "missing-sha":
				delete(r.tree, "sha")
			}
			_, err := (&GitHub{Public: r}).Fetch(t.Context(), s)
			if !errors.Is(err, ErrContentInvalid) {
				t.Fatal(err)
			}
			for _, c := range r.calls {
				if strings.Contains(c, "/blobs/") {
					t.Fatal(r.calls)
				}
			}
		})
	}
}

func TestRejectOversizedCatalog(t *testing.T) {
	for _, kind := range []string{"tree", "mismatch", "decoded", "hash", "base64"} {
		t.Run(kind, func(t *testing.T) {
			r := newFetchRig()
			entry := r.tree["tree"].([]any)[0].(map[string]any)
			want := ErrContentInvalid
			switch kind {
			case "tree":
				entry["size"] = MaxCatalogBytes + 1
				want = ErrContentTooLarge
			case "mismatch":
				r.blob["size"] = 1
			case "decoded":
				entry["size"] = MaxCatalogBytes + 1
				r.blob["size"] = MaxCatalogBytes + 1
				r.blob["content"] = base64.StdEncoding.EncodeToString(make([]byte, MaxCatalogBytes+1))
				want = ErrContentTooLarge
			case "hash":
				r.blob["content"] = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", len(r.raw))))
			case "base64":
				r.blob["content"] = "!"
			}
			_, err := (&GitHub{Public: r}).Fetch(t.Context(), initialSource())
			if !errors.Is(err, want) {
				t.Fatal(err)
			}
		})
	}
}

func TestFetchDataNeverExecuted(t *testing.T) {
	r := newFetchRig()
	raw := []byte(strings.ReplaceAll(baseJSON, "fixture-retired", "echo nope; $(false)"))
	hash := blobHash(raw)
	entry := r.tree["tree"].([]any)[0].(map[string]any)
	entry["sha"] = hash
	entry["size"] = len(raw)
	r.blob = map[string]any{"sha": hash, "size": len(raw), "encoding": "base64", "content": base64.StdEncoding.EncodeToString(raw)}
	commandCalls := 0
	private := &GHAPI{Runner: runnerFunc(func(context.Context, []string, io.Writer, io.Writer) error {
		commandCalls++
		return errors.New("execution forbidden")
	})}
	got, err := (&GitHub{Public: r, Private: private}).Fetch(t.Context(), initialSource())
	if err != nil {
		t.Fatal(err)
	}
	command, err := config.LiteralText(got.Catalog.Connections["retired"].Transport.Stdio.Command)
	if err != nil || command != "echo nope; $(false)" {
		t.Fatal(command, err)
	}
	if commandCalls != 0 {
		t.Fatal("unexpected command execution")
	}
}

func TestStage4Fixtures(t *testing.T) {
	for _, name := range []string{"base", "next", "description"} {
		raw, err := os.ReadFile("../../testdata/catalogs/stage4-" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = config.DecodeCatalog(raw); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFetchNestedTraversal(t *testing.T) {
	r := newFetchRig()
	s := initialSource()
	s.Path = "目录 space/catalog.json"
	leaf := r.tree["tree"].([]any)[0].(map[string]any)
	leaf["path"] = "catalog.json"
	leaf["mode"] = "100755"
	leafSHA := singleEntryTreeHash("catalog.json", "100755", leaf["sha"].(string))
	r.tree["sha"] = leafSHA
	rootSHA := singleEntryTreeHash("目录 space", "040000", leafSHA)
	ancestor := map[string]any{"sha": rootSHA, "truncated": false, "tree": []any{map[string]any{"path": "目录 space", "mode": "040000", "type": "tree", "sha": leafSHA}}}
	r.metadata["download_url"] = "https://must-not-follow.invalid"
	api := apiFunc(func(ctx context.Context, e string) (APIResponse, error) {
		if strings.HasSuffix(e, "/commits/main") {
			r.calls = append(r.calls, e)
			return response(map[string]any{"sha": commitA, "commit": map[string]any{"tree": map[string]any{"sha": rootSHA}}}), nil
		}
		if strings.HasSuffix(e, "/git/trees/"+rootSHA) {
			r.calls = append(r.calls, e)
			return response(ancestor), nil
		}
		return r.Get(ctx, e)
	})
	got, err := (&GitHub{Public: api}).Fetch(t.Context(), s)
	if err != nil || got.Source.Path != s.Path || len(r.calls) != 5 {
		t.Fatal(got.Source, err, r.calls)
	}
}

func TestFetchStrictWire(t *testing.T) {
	for _, tc := range []struct{ name, endpoint, raw string }{
		{"metadata-missing", "/repos/fixture-owner/fixture.repo", `{"id":42,"full_name":"fixture-owner/fixture.repo","default_branch":"main"}`},
		{"metadata-null", "/repos/fixture-owner/fixture.repo", `{"id":42,"full_name":"fixture-owner/fixture.repo","default_branch":"main","private":null}`},
		{"metadata-duplicate", "/repos/fixture-owner/fixture.repo", `{"id":42,"id":43,"full_name":"fixture-owner/fixture.repo","default_branch":"main","private":false}`},
		{"commit-missing-tree", "/commits/main", `{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","commit":{}}`},
		{"commit-uppercase", "/commits/main", `{"sha":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","commit":{"tree":{"sha":"cccccccccccccccccccccccccccccccccccccccc"}}}`},
		{"tree-null", "/git/trees/" + treeSHA, `{"sha":"cccccccccccccccccccccccccccccccccccccccc","tree":null,"truncated":false}`},
		{"tree-missing-size", "/git/trees/" + treeSHA, `{"sha":"cccccccccccccccccccccccccccccccccccccccc","tree":[{"path":"mcparcel.json","type":"blob","mode":"100644","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],"truncated":false}`},
		{"blob-missing-encoding", "/git/blobs/", `{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":0,"content":""}`},
		{"trailing", "/repos/fixture-owner/fixture.repo", `{} {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newFetchRig()
			api := apiFunc(func(ctx context.Context, e string) (APIResponse, error) {
				if strings.Contains(e, tc.endpoint) {
					r.calls = append(r.calls, e)
					return APIResponse{Status: 200, Body: []byte(tc.raw)}, nil
				}
				return r.Get(ctx, e)
			})
			_, err := (&GitHub{Public: api}).Fetch(t.Context(), initialSource())
			if !errors.Is(err, ErrContentInvalid) {
				t.Fatal(err)
			}
		})
	}
}

func TestFetchReferenceValidation(t *testing.T) {
	for _, ref := range []string{"abcdef1", strings.Repeat("a", 39), strings.Repeat("x", 1025), "a\nb"} {
		r := newFetchRig()
		s := initialSource()
		s.Ref = ref
		_, err := (&GitHub{Public: r}).Fetch(t.Context(), s)
		if !errors.Is(err, ErrContentInvalid) || len(r.calls) != 0 {
			t.Fatal(err, r.calls)
		}
	}
	r := newFetchRig()
	r.metadata["default_branch"] = "changed-default"
	s := existingSource()
	got, err := (&GitHub{Public: r}).Fetch(t.Context(), s)
	if err != nil || got.Source.Ref != "main" || !strings.HasSuffix(r.calls[1], "/commits/main") {
		t.Fatal(got.Source, err, r.calls)
	}
}

func TestFetchSingleDeadline(t *testing.T) {
	r := newFetchRig()
	var deadline time.Time
	api := apiFunc(func(ctx context.Context, e string) (APIResponse, error) {
		d, ok := ctx.Deadline()
		if !ok || time.Until(d) > FetchTimeout {
			t.Fatal("missing total deadline")
		}
		if deadline.IsZero() {
			deadline = d
		} else if !deadline.Equal(d) {
			t.Fatal("deadline reset")
		}
		return r.Get(ctx, e)
	})
	if _, err := (&GitHub{Public: api}).Fetch(t.Context(), initialSource()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r.calls = nil
	if _, err := (&GitHub{Public: r}).Fetch(ctx, initialSource()); !errors.Is(err, context.Canceled) || len(r.calls) != 0 {
		t.Fatal(err, r.calls)
	}
}

func TestValidateSnapshotStoreSize(t *testing.T) {
	c, err := config.DecodeCatalog([]byte(baseJSON))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(c)
	paper := c.Connections["paper"]
	paper.Description = strings.Repeat("x", int(MaxCatalogBytes)-len(raw)+len(paper.Description)-1)
	c.Connections["paper"] = paper
	raw, _ = json.Marshal(c)
	if int64(len(raw)) >= MaxCatalogBytes {
		t.Fatal("bad test fixture")
	}
	_, err = validateSnapshot(Snapshot{Source: existingSource(), Catalog: c})
	if !errors.Is(err, ErrContentTooLarge) {
		t.Fatal(err)
	}
}

func TestValidateSnapshotRejectsEnvRef(t *testing.T) {
	c, err := config.DecodeCatalog([]byte(`{"schemaVersion":1,"connections":{"exa":{"transport":{"type":"stdio","command":"npx","env":{"EXA_API_KEY":{"secret":"env:EXA_API_KEY"}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = validateSnapshot(Snapshot{Source: existingSource(), Catalog: c}); !errors.Is(err, ErrContentInvalid) {
		t.Fatal(err)
	}
}

func TestValidateSnapshotRejectsOAuthEnvRef(t *testing.T) {
	c, err := config.DecodeCatalog([]byte(`{"schemaVersion":1,"connections":{"x":{"transport":{"type":"http","url":"https://x.invalid/mcp"},"auth":{"type":"oauth","clientId":{"secret":"env:X"}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = validateSnapshot(Snapshot{Source: existingSource(), Catalog: c}); !errors.Is(err, ErrContentInvalid) {
		t.Fatal(err)
	}
}
