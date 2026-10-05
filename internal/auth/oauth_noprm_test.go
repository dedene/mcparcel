package auth

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The PRM shim must not drop the SDK's dial check for metadata GETs: loopback
// and public addresses only.
func TestNoPRMClientKeepsDiscoveryDialGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	h := NewOAuthHandler(OAuthOptions{})
	c := noPRMClient(h.client, "https://mcp.example/mcp")
	defer c.CloseIdleConnections()
	if c.Timeout != h.client.Timeout || c.CheckRedirect == nil || c.Transport.(*prmShim).base != h.client.Transport {
		t.Fatal("client settings not kept")
	}

	resp, err := c.Get("https://mcp.example/.well-known/oauth-protected-resource/mcp")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != `{"authorization_servers":["https://mcp.example/mcp"],"resource":"https://mcp.example/mcp"}` {
		t.Fatal(resp.StatusCode, string(body))
	}

	if resp, err := c.Get(srv.URL); err != nil {
		t.Fatal("loopback refused:", err)
	} else {
		resp.Body.Close()
	}
	for _, host := range []string{"10.0.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "[fd00::1]", "0.0.0.0"} {
		if _, err := c.Get("http://" + host + ":9/.well-known/oauth-authorization-server"); err == nil || !strings.Contains(err.Error(), "non-public ip address") {
			t.Fatal(host, err)
		}
	}
}
