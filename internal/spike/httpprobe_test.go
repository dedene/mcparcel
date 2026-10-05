package spike

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/testutil"
)

func probeClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestProbeHTTPDoesNotFollowRedirects(t *testing.T) {
	var requests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	report := ProbeHTTP(context.Background(), probeClient(), "fixture", server.URL)
	if report.InitializeStatus != http.StatusTemporaryRedirect || report.Transport != "unknown" {
		t.Fatalf("report = %+v, want status 307 and unknown transport", report)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("redirect target received %d requests, want zero", got)
	}
}

func TestProbeHTTPSlowServerReportsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	client := probeClient()
	client.Timeout = 200 * time.Millisecond

	start := time.Now()
	report := ProbeHTTP(context.Background(), client, "fixture", server.URL)
	elapsed := time.Since(start)
	if report.Error != "initialize request failed: timeout" {
		t.Fatalf("error = %q, want initialize request failed: timeout", report.Error)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("probe returned after %s, want under 2 seconds", elapsed)
	}
}

func TestProbeHTTPStreamable(t *testing.T) {
	server := testutil.NewFixtureServer()
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer httpServer.Close()

	report := ProbeHTTP(context.Background(), probeClient(), "fixture", httpServer.URL)
	if report.Transport != "streamable" || report.InitializeStatus != http.StatusOK || !report.Loopback {
		t.Fatalf("report = %+v, want a loopback streamable server answering 200", report)
	}
}

func TestProbeHTTPLegacySSE(t *testing.T) {
	server := testutil.NewFixtureServer()
	httpServer := httptest.NewServer(mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer httpServer.Close()

	report := ProbeHTTP(context.Background(), probeClient(), "fixture", httpServer.URL)
	if report.Transport != "sse" {
		t.Fatalf("report = %+v, want transport sse", report)
	}
}

func TestProbeHTTPDiscoversOAuth(t *testing.T) {
	mux := http.NewServeMux()
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	base := httpServer.URL

	writeJSON := func(w http.ResponseWriter, value any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(value)
	}
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+base+`/.well-known/oauth-protected-resource/mcp"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"resource":              base + "/mcp",
			"authorization_servers": []string{base},
			"scopes_supported":      []string{"read", "write"},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                                base,
			"authorization_endpoint":                base + "/authorize",
			"token_endpoint":                        base + "/token",
			"registration_endpoint":                 base + "/register",
			"response_types_supported":              []string{"code"},
			"code_challenge_methods_supported":      []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_post", "none"},
		})
	})

	report := ProbeHTTP(context.Background(), probeClient(), "fixture", base+"/mcp")
	want := HTTPReport{
		Name: "fixture", Scheme: "http", Loopback: true,
		InitializeStatus: http.StatusUnauthorized, Transport: "auth-required",
		Challenge: true, ResourceMetadata: true, AuthServerMeta: true, Registration: true,
		TokenAuthMethods: []string{"client_secret_post", "none"},
		PKCEMethods:      []string{"S256"}, ScopesAdvertised: 2,
	}
	if !reflect.DeepEqual(report, want) {
		t.Fatalf("report = %+v\nwant     %+v", report, want)
	}
}

func TestProbeHTTPUnreachableReportsKindOnly(t *testing.T) {
	httpServer := httptest.NewServer(http.NotFoundHandler())
	endpoint := httpServer.URL
	httpServer.Close()

	report := ProbeHTTP(context.Background(), probeClient(), "fixture", endpoint)
	if report.Error != "initialize request failed: connection refused" {
		t.Fatalf("error = %q, want the kind only, without host or port", report.Error)
	}
}

func TestProbeConfigSkipsStdioServersAndRecordsConfiguredAuth(t *testing.T) {
	server := testutil.NewFixtureServer()
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer httpServer.Close()

	config := map[string]any{"mcpServers": map[string]any{
		"local-tool": map[string]any{"command": "npx", "args": []string{"-y", "example"}},
		"plain":      map[string]any{"baseUrl": httpServer.URL},
		"keyed":      map[string]any{"baseUrl": httpServer.URL, "headers": map[string]string{"Authorization": "${EXAMPLE}"}},
		"signed-in":  map[string]any{"baseUrl": httpServer.URL, "auth": "oauth", "oauthTokenEndpointAuthMethod": "client_secret_post"},
	}}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "mcporter.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	reports, err := ProbeConfig(context.Background(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, report := range reports {
		got[report.Name] = report.ConfiguredAuth + "/" + report.ConfiguredMethod
	}
	want := map[string]string{"plain": "none/", "keyed": "header/", "signed-in": "oauth/client_secret_post"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reports = %v, want %v", got, want)
	}

	only, err := ProbeConfig(context.Background(), path, []string{"keyed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].Name != "keyed" {
		t.Fatalf("--only keyed returned %+v", only)
	}
}
