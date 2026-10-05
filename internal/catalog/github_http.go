package catalog

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

func validateEndpoint(endpoint string) error {
	if !strings.HasPrefix(endpoint, "/repos/") || strings.ContainsAny(endpoint, "\\#") || strings.IndexFunc(endpoint, unicode.IsControl) >= 0 {
		return ErrContentInvalid
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery {
		return ErrContentInvalid
	}
	decoded, err := url.PathUnescape(u.EscapedPath())
	if err != nil || strings.ContainsAny(decoded, "\\") || strings.IndexFunc(decoded, unicode.IsControl) >= 0 {
		return ErrContentInvalid
	}
	for _, part := range strings.Split(decoded, "/") {
		if part == "." || part == ".." {
			return ErrContentInvalid
		}
	}
	return nil
}

func headerSize(h http.Header) int64 {
	var n int64
	for k, vs := range h {
		for _, v := range vs {
			n += int64(len(k) + len(v) + 4)
		}
	}
	return n + 2
}

func (a *HTTPAPI) Get(ctx context.Context, endpoint string) (APIResponse, error) {
	if err := validateEndpoint(endpoint); err != nil {
		return APIResponse{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	client := http.Client{}
	if a.Client != nil {
		client = *a.Client
	}
	client.Jar = nil
	if client.Timeout <= 0 || client.Timeout > FetchTimeout {
		client.Timeout = FetchTimeout
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if client.Transport == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = nil
		tr.ResponseHeaderTimeout = 10 * time.Second
		tr.MaxResponseHeaderBytes = MaxHeaderBytes
		client.Transport = tr
	} else if tr, ok := client.Transport.(*http.Transport); ok {
		cloned := tr.Clone()
		cloned.Proxy = nil
		cloned.ResponseHeaderTimeout = 10 * time.Second
		cloned.MaxResponseHeaderBytes = MaxHeaderBytes
		client.Transport = cloned
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com"+endpoint, nil)
	if err != nil {
		return APIResponse{}, ErrContentInvalid
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	req.Header.Set("User-Agent", "mcparcel")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return APIResponse{}, ctx.Err()
		}
		if strings.Contains(err.Error(), "server response headers exceeded") {
			return APIResponse{}, ErrContentTooLarge
		}
		return APIResponse{}, ErrOffline
	}
	defer resp.Body.Close()
	if headerSize(resp.Header) > MaxHeaderBytes {
		return APIResponse{}, ErrContentTooLarge
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxAPIBytes+1))
	if ctx.Err() != nil {
		return APIResponse{}, ctx.Err()
	}
	if err != nil {
		return APIResponse{}, ErrOffline
	}
	if int64(len(raw)) > MaxAPIBytes {
		return APIResponse{}, ErrContentTooLarge
	}
	return APIResponse{Status: resp.StatusCode, Header: resp.Header.Clone(), Body: raw}, nil
}
