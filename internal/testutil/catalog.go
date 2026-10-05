package testutil

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/dedene/mcparcel/internal/catalog"
	"github.com/dedene/mcparcel/internal/config"
)

type CatalogAPI struct {
	mu        sync.Mutex                     `json:"-"`
	responses map[string]catalog.APIResponse `json:"-"`
	requests  []string                       `json:"-"`
}

func NewCatalogAPI() *CatalogAPI {
	return &CatalogAPI{responses: make(map[string]catalog.APIResponse)}
}

func cloneCatalogResponse(r catalog.APIResponse) catalog.APIResponse {
	return catalog.APIResponse{Status: r.Status, Header: r.Header.Clone(), Body: append([]byte(nil), r.Body...)}
}

func (a *CatalogAPI) Set(endpoint string, status int, header http.Header, body []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.responses == nil {
		a.responses = make(map[string]catalog.APIResponse)
	}
	a.responses[endpoint] = cloneCatalogResponse(catalog.APIResponse{Status: status, Header: header, Body: body})
}

func catalogObjectHash(kind string, raw []byte) string {
	h := sha1.New()
	_, _ = fmt.Fprintf(h, "%s %d%c", kind, len(raw), 0)
	_, _ = h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

func (a *CatalogAPI) SetSnapshot(snapshot catalog.Snapshot) error {
	if catalog.ValidateCatalogPath(snapshot.Source.Path) != nil {
		return catalog.ErrContentInvalid
	}
	raw, err := json.Marshal(config.Local{SchemaVersion: 1, Sources: []config.Source{snapshot.Source}})
	if err != nil {
		return catalog.ErrContentInvalid
	}
	local, err := config.DecodeLocal(raw)
	if err != nil {
		return catalog.ErrContentInvalid
	}
	raw, err = json.Marshal(snapshot.Catalog)
	if err != nil {
		return catalog.ErrContentInvalid
	}
	if int64(len(raw)) > catalog.MaxCatalogBytes {
		return catalog.ErrContentTooLarge
	}
	if _, err = config.DecodeCatalog(raw); err != nil {
		return catalog.ErrContentInvalid
	}
	source := local.Sources[0]
	base := "/repos/" + url.PathEscape(source.Owner) + "/" + url.PathEscape(source.Repo)
	responses := make(map[string]catalog.APIResponse)
	set := func(endpoint string, value any) {
		body, _ := json.Marshal(value)
		responses[endpoint] = catalog.APIResponse{Status: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}
	}
	blobHash := catalogObjectHash("blob", raw)
	set(base+"/git/blobs/"+blobHash, map[string]any{"sha": blobHash, "size": len(raw), "encoding": "base64", "content": base64.StdEncoding.EncodeToString(raw)})
	parts := strings.Split(source.Path, "/")
	childHash, mode, kind := blobHash, "100644", "blob"
	for i := len(parts) - 1; i >= 0; i-- {
		object, _ := hex.DecodeString(childHash)
		treeRaw := append([]byte(strings.TrimLeft(mode, "0")+" "+parts[i]+"\x00"), object...)
		treeHash := catalogObjectHash("tree", treeRaw)
		entry := map[string]any{"path": parts[i], "mode": mode, "type": kind, "sha": childHash}
		if kind == "blob" {
			entry["size"] = len(raw)
		}
		set(base+"/git/trees/"+treeHash, map[string]any{"sha": treeHash, "tree": []any{entry}, "truncated": false})
		childHash, mode, kind = treeHash, "040000", "tree"
	}
	commit := map[string]any{"sha": source.Commit, "commit": map[string]any{"tree": map[string]any{"sha": childHash}}}
	set(base+"/commits/"+url.PathEscape(source.Ref), commit)
	set(base+"/commits/"+source.Commit, commit)
	set(base, map[string]any{"id": source.RepositoryID, "full_name": source.Owner + "/" + source.Repo, "default_branch": source.Ref, "private": false})
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.responses == nil {
		a.responses = make(map[string]catalog.APIResponse)
	}
	for endpoint, response := range responses {
		a.responses[endpoint] = response
	}
	return nil
}

func (a *CatalogAPI) Requests() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.requests...)
}

func (a *CatalogAPI) Get(ctx context.Context, endpoint string) (catalog.APIResponse, error) {
	if err := ctx.Err(); err != nil {
		return catalog.APIResponse{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, endpoint)
	if response, ok := a.responses[endpoint]; ok {
		return cloneCatalogResponse(response), nil
	}
	return catalog.APIResponse{Status: http.StatusNotFound, Header: http.Header{}, Body: []byte(`{}`)}, nil
}

func (a *CatalogAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	response, err := a.Get(r.Context(), r.URL.RequestURI())
	if err != nil {
		return
	}
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.Status)
	_, _ = w.Write(response.Body)
}
