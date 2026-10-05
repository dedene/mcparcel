package catalog

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/jsonutil"
)

var (
	fullSHA        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	abbreviatedSHA = regexp.MustCompile(`^[0-9a-fA-F]{7,39}$`)
)

func ValidateRepositoryName(name string) (owner, repo string, err error) {
	parts := strings.Split(name, "/")
	if len(parts) != 2 {
		return "", "", ErrContentInvalid
	}
	owner, repo = strings.ToLower(parts[0]), strings.ToLower(parts[1])
	if _, err = config.CanonicalGitHub(owner, repo, "probe"); err != nil {
		return "", "", ErrContentInvalid
	}
	return owner, repo, nil
}

func ValidateCatalogPath(name string) error {
	if name == "" || len(name) > 4096 || strings.ContainsAny(name, "\\%") {
		return ErrContentInvalid
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return ErrContentInvalid
	}
	parts := strings.Split(name, "/")
	if len(parts) > 32 {
		return ErrContentInvalid
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return ErrContentInvalid
		}
	}
	return nil
}

func verifyRepository(source config.Source, id uint64, fullName string) error {
	owner, repo, err := ValidateRepositoryName(fullName)
	if err != nil || id == 0 || id > config.MaxRevision {
		return ErrContentInvalid
	}
	if source.RepositoryID != 0 && source.RepositoryID != id {
		return ErrRepositoryReused
	}
	if owner != source.Owner || repo != source.Repo {
		return ErrSourceRenamed
	}
	return nil
}

func readCatalogBlob(entry gitEntry, blob gitBlob) ([]byte, error) {
	if entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") ||
		entry.Size == nil || *entry.Size < 0 || blob.Size < 0 {
		return nil, ErrContentInvalid
	}
	if *entry.Size > MaxCatalogBytes || blob.Size > MaxCatalogBytes {
		return nil, ErrContentTooLarge
	}
	if blob.Size != *entry.Size || blob.Encoding != "base64" || blob.SHA != entry.SHA {
		return nil, ErrContentInvalid
	}
	if int64(len(blob.Content)) > MaxAPIBytes {
		return nil, ErrContentTooLarge
	}
	raw, err := base64.StdEncoding.DecodeString(blob.Content)
	if err != nil {
		return nil, ErrContentInvalid
	}
	if int64(len(raw)) > MaxCatalogBytes {
		return nil, ErrContentTooLarge
	}
	if int64(len(raw)) != blob.Size {
		return nil, ErrContentInvalid
	}
	return raw, nil
}

func validateSnapshot(s Snapshot) (Snapshot, error) {
	if err := ValidateCatalogPath(s.Source.Path); err != nil {
		return Snapshot{}, err
	}
	if len(s.Source.Ref) > 1024 || strings.IndexFunc(s.Source.Ref, unicode.IsControl) >= 0 {
		return Snapshot{}, ErrContentInvalid
	}
	raw, err := json.Marshal(config.Local{SchemaVersion: 1, Sources: []config.Source{s.Source}})
	if err != nil {
		return Snapshot{}, ErrContentInvalid
	}
	local, err := config.DecodeLocal(raw)
	if err != nil {
		return Snapshot{}, ErrContentInvalid
	}
	raw, err = json.Marshal(s.Catalog)
	if err != nil {
		return Snapshot{}, ErrContentInvalid
	}
	if int64(len(raw)) > MaxCatalogBytes {
		return Snapshot{}, ErrContentTooLarge
	}
	catalog, err := config.DecodeCatalog(raw)
	if err != nil {
		return Snapshot{}, ErrContentInvalid
	}
	for _, c := range catalog.Connections {
		if len(config.EnvRefs(c)) > 0 {
			return Snapshot{}, ErrContentInvalid
		}
	}
	raw, err = json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return Snapshot{}, ErrContentInvalid
	}
	if int64(len(raw)+1) > MaxCatalogBytes {
		return Snapshot{}, ErrContentTooLarge
	}
	return Snapshot{Source: local.Sources[0], Catalog: catalog}, nil
}

// Upstream documents are open to additional fields, but every consumed field
// must have its declared type and be present (except a non-blob entry's size).
func wireShape(v any, typ reflect.Type) bool {
	if typ.Kind() == reflect.Pointer {
		return v != nil && wireShape(v, typ.Elem())
	}
	switch typ.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return false
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			key := f.Tag.Get("json")
			for candidate := range obj {
				if candidate != key && strings.EqualFold(candidate, key) {
					return false
				}
			}
			x, exists := obj[key]
			if !exists && typ == reflect.TypeFor[gitEntry]() && key == "size" && obj["type"] != "blob" {
				continue
			}
			if !exists || !wireShape(x, f.Type) {
				return false
			}
		}
		return true
	case reflect.Slice:
		arr, ok := v.([]any)
		if !ok {
			return false
		}
		for _, x := range arr {
			if !wireShape(x, typ.Elem()) {
				return false
			}
		}
		return true
	case reflect.String:
		_, ok := v.(string)
		return ok
	case reflect.Bool:
		_, ok := v.(bool)
		return ok
	case reflect.Uint64:
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		_, err := strconv.ParseUint(string(n), 10, 64)
		return err == nil
	case reflect.Int64:
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		_, err := strconv.ParseInt(string(n), 10, 64)
		return err == nil
	default:
		return false
	}
}

func decodeWire(raw []byte, dest any) error {
	if int64(len(raw)) > MaxAPIBytes {
		return ErrContentTooLarge
	}
	v, err := jsonutil.Decode(raw)
	if err != nil || !wireShape(v, reflect.TypeOf(dest).Elem()) {
		return ErrContentInvalid
	}
	exact, err := json.Marshal(exactWireFields(v, reflect.TypeOf(dest).Elem()))
	if err != nil || json.Unmarshal(exact, dest) != nil {
		return ErrContentInvalid
	}
	return nil
}

// Project validated fields so struct decoding cannot consume unvalidated keys.
func exactWireFields(v any, typ reflect.Type) any {
	if typ.Kind() == reflect.Pointer {
		return exactWireFields(v, typ.Elem())
	}
	switch typ.Kind() {
	case reflect.Struct:
		obj := v.(map[string]any)
		exact := make(map[string]any, typ.NumField())
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			key := f.Tag.Get("json")
			if value, exists := obj[key]; exists {
				exact[key] = exactWireFields(value, f.Type)
			}
		}
		return exact
	case reflect.Slice:
		arr := v.([]any)
		exact := make([]any, len(arr))
		for i, value := range arr {
			exact[i] = exactWireFields(value, typ.Elem())
		}
		return exact
	default:
		return v
	}
}

func rateLimited(resp APIResponse) bool {
	if resp.Status == 429 {
		return true
	}
	if resp.Status != 403 {
		return false
	}
	if resp.Header.Get("Retry-After") != "" || resp.Header.Get("X-RateLimit-Remaining") == "0" {
		return true
	}
	v, err := jsonutil.Decode(resp.Body)
	if err != nil {
		return false
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return false
	}
	message, _ := obj["message"].(string)
	return strings.Contains(strings.ToLower(message), "secondary rate limit")
}

func responseError(resp APIResponse) error {
	if rateLimited(resp) {
		return ErrRateLimited
	}
	switch {
	case resp.Status >= 200 && resp.Status < 300:
		return nil
	case resp.Status >= 300 && resp.Status < 400:
		return ErrSourceRenamed
	case resp.Status == 401 || resp.Status == 403:
		return ErrUnauthorized
	case resp.Status == 404:
		return ErrRepositoryMissing
	case resp.Status >= 500:
		return ErrOffline
	default:
		return ErrContentInvalid
	}
}

func apiGet(ctx context.Context, api API, endpoint string) (APIResponse, error) {
	if ctx.Err() != nil {
		return APIResponse{}, ctx.Err()
	}
	if api == nil {
		return APIResponse{}, ErrGHRequired
	}
	resp, err := api.Get(ctx, endpoint)
	if ctx.Err() != nil {
		return APIResponse{}, ctx.Err()
	}
	if err != nil {
		switch err {
		case ErrOffline, ErrUnauthorized, ErrGHRequired, ErrContentInvalid, ErrContentTooLarge:
			return APIResponse{}, err
		}
		return APIResponse{}, ErrOffline
	}
	if int64(len(resp.Body)) > MaxAPIBytes || headerSize(resp.Header) > MaxHeaderBytes {
		return APIResponse{}, ErrContentTooLarge
	}
	return resp, nil
}

func getWire(ctx context.Context, api API, endpoint string, dest any) error {
	resp, err := apiGet(ctx, api, endpoint)
	if err != nil {
		return err
	}
	if err = responseError(resp); err != nil {
		return err
	}
	return decodeWire(resp.Body, dest)
}

func (g *GitHub) Fetch(ctx context.Context, source config.Source) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	owner, repo, err := ValidateRepositoryName(source.Owner + "/" + source.Repo)
	if err != nil {
		return Snapshot{}, err
	}
	initial := source.RepositoryID == 0 && source.ID == "" && source.Commit == "" && !source.Pinned
	if !initial {
		raw, e := json.Marshal(config.Local{SchemaVersion: 1, Sources: []config.Source{source}})
		if e != nil {
			return Snapshot{}, ErrContentInvalid
		}
		if _, e = config.DecodeLocal(raw); e != nil {
			return Snapshot{}, ErrContentInvalid
		}
	}
	source.Owner, source.Repo = owner, repo
	if err = ValidateCatalogPath(source.Path); err != nil {
		return Snapshot{}, err
	}
	if len(source.Ref) > 1024 || strings.IndexFunc(source.Ref, unicode.IsControl) >= 0 || abbreviatedSHA.MatchString(source.Ref) {
		return Snapshot{}, ErrContentInvalid
	}
	if initial && fullSHA.MatchString(strings.ToLower(source.Ref)) {
		source.Ref = strings.ToLower(source.Ref)
		source.Pinned = true
	}
	base := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
	api := g.Public
	resp, err := apiGet(ctx, api, base)
	if err != nil {
		return Snapshot{}, err
	}
	var metadata repositoryResponse
	fallback := !rateLimited(resp) && (resp.Status == 401 || resp.Status == 403 || resp.Status == 404)
	if !fallback {
		if err = responseError(resp); err != nil {
			return Snapshot{}, err
		}
		if err = decodeWire(resp.Body, &metadata); err != nil {
			return Snapshot{}, err
		}
		fallback = metadata.Private
	}
	if fallback {
		api = g.Private
		if err = getWire(ctx, api, base, &metadata); err != nil {
			return Snapshot{}, err
		}
	}
	if err = verifyRepository(source, metadata.ID, metadata.FullName); err != nil {
		return Snapshot{}, err
	}
	if source.Ref == "" {
		source.Ref = metadata.DefaultBranch
	}
	if source.Ref == "" || len(source.Ref) > 1024 || strings.IndexFunc(source.Ref, unicode.IsControl) >= 0 {
		return Snapshot{}, ErrContentInvalid
	}
	ref := source.Ref
	if source.Pinned && source.Commit != "" {
		ref = source.Commit
	}
	var commit commitResponse
	if err = getWire(ctx, api, base+"/commits/"+url.PathEscape(ref), &commit); err != nil {
		return Snapshot{}, err
	}
	if !fullSHA.MatchString(commit.SHA) || !fullSHA.MatchString(commit.Commit.Tree.SHA) ||
		source.Pinned && commit.SHA != strings.ToLower(ref) {
		return Snapshot{}, ErrContentInvalid
	}
	treeHash := commit.Commit.Tree.SHA
	parts := strings.Split(source.Path, "/")
	var final gitEntry
	for i, part := range parts {
		var tree treeResponse
		if err = getWire(ctx, api, base+"/git/trees/"+treeHash, &tree); err != nil {
			return Snapshot{}, err
		}
		if tree.Truncated || tree.SHA != treeHash {
			return Snapshot{}, ErrContentInvalid
		}
		seen := map[string]bool{}
		found := false
		for _, entry := range tree.Tree {
			if entry.Path == "" || strings.Contains(entry.Path, "/") || entry.Path == "." || entry.Path == ".." || seen[entry.Path] || !fullSHA.MatchString(entry.SHA) {
				return Snapshot{}, ErrContentInvalid
			}
			seen[entry.Path] = true
			if entry.Type == "blob" && (entry.Size == nil || *entry.Size < 0) {
				return Snapshot{}, ErrContentInvalid
			}
			if entry.Path == part {
				final = entry
				found = true
			}
		}
		if !found {
			return Snapshot{}, ErrContentInvalid
		}
		if i < len(parts)-1 {
			if final.Type != "tree" || final.Mode != "040000" {
				return Snapshot{}, ErrContentInvalid
			}
			treeHash = final.SHA
		} else {
			if final.Type != "blob" || (final.Mode != "100644" && final.Mode != "100755") || final.Size == nil {
				return Snapshot{}, ErrContentInvalid
			}
			if *final.Size > MaxCatalogBytes {
				return Snapshot{}, ErrContentTooLarge
			}
		}
	}
	var blob gitBlob
	if err = getWire(ctx, api, base+"/git/blobs/"+final.SHA, &blob); err != nil {
		return Snapshot{}, err
	}
	raw, err := readCatalogBlob(final, blob)
	if err != nil {
		return Snapshot{}, err
	}
	h := sha1.New()
	_, _ = fmt.Fprintf(h, "blob %d%c", len(raw), 0)
	_, _ = h.Write(raw)
	if hex.EncodeToString(h.Sum(nil)) != final.SHA {
		return Snapshot{}, ErrContentInvalid
	}
	if !utf8.Valid(raw) {
		return Snapshot{}, ErrContentInvalid
	}
	catalog, err := config.DecodeCatalog(raw)
	if err != nil {
		return Snapshot{}, ErrContentInvalid
	}
	source.ID = "github-" + strconv.FormatUint(metadata.ID, 10)
	source.RepositoryID = metadata.ID
	source.Commit = commit.SHA
	return validateSnapshot(Snapshot{Source: source, Catalog: catalog})
}
