//go:build mcparceltest

package cmd

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dedene/mcparcel/internal/catalog"
	"github.com/dedene/mcparcel/internal/config"
)

type catalogFetcherError struct {
	err error `json:"-"`
}

func (f catalogFetcherError) Fetch(context.Context, config.Source) (catalog.Snapshot, error) {
	return catalog.Snapshot{}, f.err
}

type catalogFixtureRoundTripper func(*http.Request) (*http.Response, error)

func (f catalogFixtureRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// catalogFetcherDeadline lets the black-box timeout test end before the
// production catalog.FetchTimeout of 30 s.
type catalogFetcherDeadline struct {
	catalog.Fetcher
	timeout time.Duration
}

func (f catalogFetcherDeadline) Fetch(ctx context.Context, source config.Source) (catalog.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	return f.Fetcher.Fetch(ctx, source)
}

func newCatalogFetcher() catalog.Fetcher {
	fetcher := newFixtureGitHub()
	if d, err := time.ParseDuration(os.Getenv("MCPARCEL_TEST_FETCH_TIMEOUT")); err == nil && d > 0 {
		return catalogFetcherDeadline{Fetcher: fetcher, timeout: d}
	}
	return fetcher
}

func newFixtureGitHub() catalog.Fetcher {
	raw := os.Getenv("MCPARCEL_TEST_GITHUB_API")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Opaque != "" || u.User != nil ||
		u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		strings.Contains(raw, "#") ||
		(u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") {
		return catalogFetcherError{err: catalog.ErrOffline}
	}
	port, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil || port == 0 {
		return catalogFetcherError{err: catalog.ErrOffline}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.MaxResponseHeaderBytes = catalog.MaxHeaderBytes
	transport.ResponseHeaderTimeout = 10 * time.Second
	api := &catalog.HTTPAPI{Client: &http.Client{Transport: catalogFixtureRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.github.com" || r.URL.User != nil {
			return nil, catalog.ErrOffline
		}
		rewritten := r.Clone(r.Context())
		rewritten.URL.Scheme, rewritten.URL.Host = u.Scheme, u.Host
		rewritten.Host = u.Host
		return transport.RoundTrip(rewritten)
	})}}
	return &catalog.GitHub{Public: api, Private: api}
}
