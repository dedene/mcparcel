package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// discoverIssuer mirrors the SDK's protected-resource discovery order
// (challenge URL, path-inserted, root) so the binding stored in the Keychain
// matches what the SDK uses. Without metadata the issuer is the origin and
// found is false.
func discoverIssuer(ctx context.Context, c *http.Client, mcpURL string, wwwAuth []string) (issuer, resource string, asm *oauthex.AuthServerMeta, found bool, err error) {
	u, err := url.Parse(mcpURL)
	if err != nil {
		return "", "", nil, false, err
	}
	type candidate struct{ url, resource string }
	var candidates []candidate
	if challenges, err := oauthex.ParseWWWAuthenticate(wwwAuth); err == nil {
		for _, ch := range challenges {
			if m := ch.Params["resource_metadata"]; m != "" {
				candidates = append(candidates, candidate{m, mcpURL})
				break
			}
		}
	}
	m := *u
	m.Path = "/.well-known/oauth-protected-resource/" + strings.TrimLeft(u.Path, "/")
	candidates = append(candidates, candidate{m.String(), mcpURL})
	m.Path = "/.well-known/oauth-protected-resource"
	root := *u
	root.Path = ""
	candidates = append(candidates, candidate{m.String(), root.String()})

	issuer, resource = root.String(), mcpURL
	for _, cand := range candidates {
		prm, err := oauthex.GetProtectedResourceMetadata(ctx, cand.url, cand.resource, c)
		if err != nil || prm == nil {
			continue
		}
		if len(prm.AuthorizationServers) == 0 {
			return "", "", nil, false, errors.New("protected resource metadata names no authorization server")
		}
		issuer, resource, found = prm.AuthorizationServers[0], prm.Resource, true
		break
	}
	asm, err = sdkauth.GetAuthServerMetadata(ctx, issuer, c)
	if err != nil {
		return "", "", nil, false, err
	}
	return issuer, resource, asm, found, nil
}

// checkRedirect never follows a redirect of a token, registration or other
// non-GET request, so a refresh token, client secret or code verifier is never
// sent to another URL. Metadata GETs keep the SDK's discovery rules, which a
// client's CheckRedirect replaces: at most 10 hops, no https downgrade, never
// into loopback.
func checkRedirect(req *http.Request, via []*http.Request) error {
	switch host := req.URL.Hostname(); {
	case via[0].Method != http.MethodGet:
		return http.ErrUseLastResponse
	case len(via) >= 10:
		return errors.New("too many redirects")
	case via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https":
		return errors.New("redirect downgrades https")
	case host == "localhost" || net.ParseIP(host).IsLoopback():
		return errors.New("redirect into loopback")
	}
	return nil
}

func issuersEqual(a, b string) bool {
	return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/")
}
