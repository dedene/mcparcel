package catalog

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/dedene/mcparcel/internal/config"
)

const (
	MaxCatalogBytes int64         = 2 * 1024 * 1024
	MaxAPIBytes     int64         = 4 * 1024 * 1024
	MaxHeaderBytes  int64         = 64 * 1024
	FetchTimeout    time.Duration = 30 * time.Second
)

var (
	ErrOffline           = errors.New("catalog network unavailable")
	ErrUnauthorized      = errors.New("catalog authorization required")
	ErrGHRequired        = errors.New("authenticated gh required")
	ErrRateLimited       = errors.New("catalog rate limited")
	ErrRepositoryMissing = errors.New("catalog repository unavailable")
	ErrSourceRenamed     = errors.New("catalog repository renamed")
	ErrRepositoryReused  = errors.New("catalog repository identity changed")
	ErrSourceConflict    = errors.New("catalog source already registered differently")
	ErrContentInvalid    = errors.New("invalid catalog content")
	ErrContentTooLarge   = errors.New("catalog content exceeds size limit")
	ErrLocalBindings     = errors.New("catalog update conflicts with local bindings")
)

type APIResponse struct {
	Status int         `json:"-"`
	Header http.Header `json:"-"`
	Body   []byte      `json:"-"`
}
type (
	API interface {
		Get(context.Context, string) (APIResponse, error)
	}
	HTTPAPI struct {
		Client *http.Client `json:"-"`
	}
	GHRunner interface {
		Run(context.Context, []string, io.Writer, io.Writer) error
	}
	ExecGH struct{}
	GHAPI  struct {
		Runner GHRunner `json:"-"`
	}
	Snapshot struct {
		Source  config.Source  `json:"source"`
		Catalog config.Catalog `json:"-"`
	}
)

type (
	Fetcher interface {
		Fetch(context.Context, config.Source) (Snapshot, error)
	}
	GitHub struct {
		Public  API `json:"-"`
		Private API `json:"-"`
	}
)

func NewGitHub() *GitHub { return &GitHub{Public: &HTTPAPI{}, Private: &GHAPI{Runner: ExecGH{}}} }

type repositoryResponse struct {
	ID            uint64 `json:"id"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}
type gitObject struct {
	SHA string `json:"sha"`
}
type commitResponse struct {
	SHA    string `json:"sha"`
	Commit struct {
		Tree gitObject `json:"tree"`
	} `json:"commit"`
}
type gitEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size *int64 `json:"size"`
}
type treeResponse struct {
	SHA       string     `json:"sha"`
	Tree      []gitEntry `json:"tree"`
	Truncated bool       `json:"truncated"`
}
type gitBlob struct {
	SHA      string `json:"sha"`
	Size     int64  `json:"size"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}
