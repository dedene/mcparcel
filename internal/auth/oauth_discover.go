package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// discoverIssuer mirrors the SDK's protected-resource discovery order
// (challenge URL, path-inserted, root) so the binding stored in the Keychain
// matches what the SDK uses. Without metadata the issuer is the origin and
// found is false; when the origin has no valid authorization-server metadata,
// the MCP URL itself is tried as issuer (see noPRMClient).
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
	if !found && (err != nil || asm == nil) && strings.Trim(u.Path, "/") != "" {
		// Some servers publish no PRM and name the MCP URL itself as issuer.
		if m, err := sdkauth.GetAuthServerMetadata(ctx, mcpURL, c); err == nil && m != nil {
			return mcpURL, mcpURL, m, false, nil
		}
	}
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

// noPRMClient is c for a sign-in whose issuer is the MCP URL without PRM: the
// SDK takes the authorization server only from PRM or else the origin, so the
// path-inserted PRM URL answers with a document naming mcpURL. A wrapped
// transport opts out of the SDK's non-public-address dial check for metadata
// GETs, so those dial through a copy of it; other requests use c's transport.
func noPRMClient(c *http.Client, mcpURL string) *http.Client {
	u, _ := url.Parse(mcpURL)
	u.Path = "/.well-known/oauth-protected-resource/" + strings.TrimLeft(u.Path, "/")
	doc, _ := json.Marshal(map[string]any{"resource": mcpURL, "authorization_servers": []string{mcpURL}})
	base := c.Transport.(*http.Transport)
	guarded := base.Clone()
	guarded.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: publicDial}).DialContext
	return &http.Client{Timeout: c.Timeout, CheckRedirect: c.CheckRedirect, Transport: &prmShim{prmURL: u.String(), doc: doc, base: base, guarded: guarded}}
}

type prmShim struct {
	prmURL        string
	doc           []byte
	base, guarded *http.Transport
}

func (s *prmShim) RoundTrip(r *http.Request) (*http.Response, error) {
	switch {
	case r.Method != http.MethodGet:
		return s.base.RoundTrip(r)
	case r.URL.String() != s.prmURL:
		return s.guarded.RoundTrip(r)
	}
	return &http.Response{
		Status: "200 OK", StatusCode: 200, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Request: r,
		Header: http.Header{"Content-Type": {"application/json"}}, ContentLength: int64(len(s.doc)), Body: io.NopCloser(bytes.NewReader(s.doc)),
	}, nil
}

func (s *prmShim) CloseIdleConnections() { s.guarded.CloseIdleConnections() }

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// publicDial is the SDK's discovery dial check (oauthex newDiscoveryTransport):
// IP addresses only, none private, link-local, CGNAT, multicast or unspecified.
// Loopback passes, as there.
func publicDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("non-ip address: %w", err)
	}
	if ip = ip.Unmap(); !ip.IsValid() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() || cgnat.Contains(ip) {
		return fmt.Errorf("non-public ip address: %q", ip)
	}
	return nil
}
