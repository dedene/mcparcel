package auth

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
)

// maxRegistrationBytes matches the SDK's cap on a registration response.
const maxRegistrationBytes = 1 << 20

// withPublicClientDefault wraps c so that a successful dynamic registration at
// endpoint that issues no client_secret and names no
// token_endpoint_auth_method reads as "none". The SDK applies RFC 7591's
// client_secret_basic default otherwise, and a client without a secret then
// sends a Basic header with an empty one, which some servers (Raindrop.io)
// refuse at the token endpoint. Every other request passes through unchanged.
func withPublicClientDefault(c *http.Client, endpoint string) *http.Client {
	u, err := url.Parse(endpoint)
	if err != nil || endpoint == "" {
		return c
	}
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	return &http.Client{Timeout: c.Timeout, CheckRedirect: c.CheckRedirect, Jar: c.Jar, Transport: &publicClientShim{endpoint: u.String(), base: base}}
}

type publicClientShim struct {
	endpoint string
	base     http.RoundTripper
}

func (s *publicClientShim) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := s.base.RoundTrip(r)
	if err != nil || r.Method != http.MethodPost || r.URL.String() != s.endpoint || resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return resp, err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRegistrationBytes+1))
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if len(body) <= maxRegistrationBytes {
		body = publicClientDefault(body)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Del("Content-Length")
	return resp, nil
}

func (s *publicClientShim) CloseIdleConnections() {
	if c, ok := s.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// publicClientDefault sets token_endpoint_auth_method to "none" in a
// registration response that has neither it nor a client_secret. Anything it
// cannot read is returned unchanged, for the SDK to judge.
func publicClientDefault(body []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil || m == nil {
		return body
	}
	for _, key := range []string{"token_endpoint_auth_method", "client_secret"} {
		var v string
		if raw, ok := m[key]; ok && (json.Unmarshal(raw, &v) != nil || v != "") {
			return body
		}
	}
	m["token_endpoint_auth_method"] = json.RawMessage(`"none"`)
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}
