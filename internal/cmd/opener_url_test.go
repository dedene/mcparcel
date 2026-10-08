//go:build !mcparceltest

package cmd

import "testing"

// Only https, or http on a loopback host, made of plain URL characters
// reaches a browser opener; shell metacharacters never do.
func TestValidSignInURL(t *testing.T) {
	for _, raw := range []string{
		"https://as.example/authorize?response_type=code&client_id=abc&redirect_uri=http%3A%2F%2F127.0.0.1%3A53682%2Fcallback&state=x-y_z~1&scope=a%20b",
		"http://127.0.0.1:53682/callback",
		"http://localhost:8080/authorize",
		"http://[::1]:8080/authorize",
		"https://as.example/p;x=1/a@b!c*d+e,f#frag",
	} {
		if err := validSignInURL(raw); err != nil {
			t.Error(raw, err)
		}
	}
	for _, raw := range []string{
		"http://as.example/authorize",
		"file:///etc/passwd",
		"javascript:alert(1)",
		"https:///nohost",
		"https://as.example/$(id)",
		"https://as.example/`id`",
		"https://as.example/a'b",
		`https://as.example/a"b`,
		"https://as.example/a b",
		`https://as.example/a\b`,
		"https://as.example/a<b>",
		"https://as.example/{a}|^",
		"https://as.example/(a)",
		"https://as.example/a\nb",
		"https://as.example/\x00",
		"https://as.example/é",
	} {
		if err := validSignInURL(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}
