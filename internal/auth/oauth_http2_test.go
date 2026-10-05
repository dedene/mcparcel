package auth

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The SDK clones the handler's transport and sets a DialContext for metadata
// lookups. Without ForceAttemptHTTP2 that clone cannot talk to an HTTP/2
// server, and discovery fails against real providers.
func TestOAuthClientSpeaksHTTP2ThroughSDKClone(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Proto", r.Proto)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	base := NewOAuthHandler(OAuthOptions{}).client.Transport.(*http.Transport)
	clone := base.Clone()
	clone.TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	clone.DialContext = (&net.Dialer{}).DialContext

	resp, err := (&http.Client{Transport: clone}).Get(srv.URL)
	if err != nil {
		t.Fatalf("GET over the cloned transport: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Proto"); got != "HTTP/2.0" {
		t.Fatalf("protocol = %q, want HTTP/2.0", got)
	}
}
