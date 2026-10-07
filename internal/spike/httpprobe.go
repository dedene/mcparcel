//go:build darwin

package spike

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// HTTPReport is one row of the HTTP/OAuth probe. It holds no URL, token or
// header value, so rows can be pasted into docs/feasibility.md.
type HTTPReport struct {
	Name             string   `json:"name"`
	ConfiguredAuth   string   `json:"configuredAuth"` // oauth, header or none
	ConfiguredMethod string   `json:"configuredTokenEndpointAuthMethod,omitempty"`
	Scheme           string   `json:"scheme"`
	Loopback         bool     `json:"loopback"`
	InitializeStatus int      `json:"initializeStatus"`
	Transport        string   `json:"transport"` // streamable, sse, auth-required or unknown
	Challenge        bool     `json:"wwwAuthenticate"`
	ResourceMetadata bool     `json:"protectedResourceMetadata"`
	AuthServerMeta   bool     `json:"authServerMetadata"`
	Registration     bool     `json:"dynamicRegistration"`
	ClientIDMetadata bool     `json:"clientIdMetadataDocument"`
	TokenAuthMethods []string `json:"tokenEndpointAuthMethods,omitempty"`
	PKCEMethods      []string `json:"codeChallengeMethods,omitempty"`
	ScopesAdvertised int      `json:"scopesAdvertised"`
	Error            string   `json:"error,omitempty"`
}

type mcporterServer struct {
	BaseURL    string                     `json:"baseUrl"`
	Auth       string                     `json:"auth"`
	Headers    map[string]json.RawMessage `json:"headers"`
	AuthMethod string                     `json:"oauthTokenEndpointAuthMethod"`
}

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcparcel-probe","version":"0"}}}`

// ProbeConfig probes every HTTP server in an mcporter config file without
// credentials. It never registers a client and never requests a token.
func ProbeConfig(ctx context.Context, path string, only []string) ([]HTTPReport, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config struct {
		Servers map[string]mcporterServer `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	names := make([]string, 0, len(config.Servers))
	for name, server := range config.Servers {
		if server.BaseURL == "" {
			continue
		}
		if len(only) > 0 && !slices.Contains(only, name) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	client := &http.Client{
		Timeout: 15 * time.Second,
		// Report the first response; a redirect is a finding, not something to follow.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	reports := make([]HTTPReport, 0, len(names))
	for _, name := range names {
		server := config.Servers[name]
		report := ProbeHTTP(ctx, client, name, server.BaseURL)
		report.ConfiguredMethod = server.AuthMethod
		switch {
		case server.Auth == "oauth":
			report.ConfiguredAuth = "oauth"
		case len(server.Headers) > 0:
			report.ConfiguredAuth = "header"
		default:
			report.ConfiguredAuth = "none"
		}
		reports = append(reports, report)
	}
	return reports, nil
}

// ProbeHTTP sends one unauthenticated initialize request and, when the server
// answers 401, follows OAuth discovery.
func ProbeHTTP(ctx context.Context, client *http.Client, name, endpoint string) HTTPReport {
	report := HTTPReport{Name: name, Transport: "unknown"}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		report.Error = "invalid URL"
		return report
	}
	report.Scheme = parsed.Scheme
	if ip := net.ParseIP(parsed.Hostname()); (ip != nil && ip.IsLoopback()) || parsed.Hostname() == "localhost" {
		report.Loopback = true
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(initializeBody))
	if err != nil {
		report.Error = "invalid request"
		return report
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response, err := client.Do(request)
	if err != nil {
		report.Error = "initialize request failed: " + errorKind(err)
		return report
	}
	response.Body.Close()
	report.InitializeStatus = response.StatusCode

	switch response.StatusCode {
	case http.StatusOK:
		report.Transport = "streamable"
		if session := response.Header.Get("Mcp-Session-Id"); session != "" {
			closeSession(ctx, client, endpoint, session)
		}
	case http.StatusUnauthorized:
		report.Transport = "auth-required"
		discoverOAuth(ctx, client, endpoint, response, &report)
	default:
		if legacySSE(ctx, client, endpoint) {
			report.Transport = "sse"
		}
	}
	return report
}

func discoverOAuth(ctx context.Context, client *http.Client, endpoint string, response *http.Response, report *HTTPReport) {
	headers := response.Header.Values("WWW-Authenticate")
	report.Challenge = len(headers) > 0

	parsed, _ := url.Parse(endpoint)
	origin := parsed.Scheme + "://" + parsed.Host
	candidates := []string{
		origin + "/.well-known/oauth-protected-resource" + strings.TrimSuffix(parsed.Path, "/"),
		origin + "/.well-known/oauth-protected-resource",
	}
	if challenges, err := oauthex.ParseWWWAuthenticate(headers); err == nil {
		for _, challenge := range challenges {
			if metadataURL := challenge.Params["resource_metadata"]; metadataURL != "" {
				candidates = append([]string{metadataURL}, candidates...)
			}
		}
	}

	issuer := origin // 2025-03-26 servers publish authorization metadata on their own origin
	for _, candidate := range candidates {
		resource, err := oauthex.GetProtectedResourceMetadata(ctx, candidate, endpoint, client)
		if err != nil || resource == nil {
			continue
		}
		report.ResourceMetadata = true
		report.ScopesAdvertised = len(resource.ScopesSupported)
		if len(resource.AuthorizationServers) > 0 {
			issuer = resource.AuthorizationServers[0]
		}
		break
	}

	meta, err := auth.GetAuthServerMetadata(ctx, issuer, client)
	if err != nil || meta == nil {
		report.Error = "no authorization server metadata"
		return
	}
	report.AuthServerMeta = true
	report.Registration = meta.RegistrationEndpoint != ""
	report.ClientIDMetadata = meta.ClientIDMetadataDocumentSupported
	report.TokenAuthMethods = meta.TokenEndpointAuthMethodsSupported
	report.PKCEMethods = meta.CodeChallengeMethodsSupported
	if report.ScopesAdvertised == 0 {
		report.ScopesAdvertised = len(meta.ScopesSupported)
	}
}

// legacySSE reports whether a GET answers with an event stream, which is how
// the 2024-11-05 SSE transport starts.
func legacySSE(ctx context.Context, client *http.Client, endpoint string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK &&
		strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream")
}

func closeSession(ctx context.Context, client *http.Client, endpoint, session string) {
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return
	}
	request.Header.Set("Mcp-Session-Id", session)
	if response, err := client.Do(request); err == nil {
		response.Body.Close()
	}
}

// errorKind keeps hostnames and URLs out of the report.
func errorKind(err error) string {
	var netErr net.Error
	switch {
	case errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	case strings.Contains(err.Error(), "no such host"):
		return "dns"
	case strings.Contains(err.Error(), "connection refused"):
		return "connection refused"
	case strings.Contains(err.Error(), "certificate"):
		return "tls"
	default:
		return "network"
	}
}
