package auth

import (
	"encoding/json"
	"testing"
)

func TestPublicClientDefault(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string // want "" means unchanged
	}{
		{"no method no secret", `{"client_id":"c"}`, "none"},
		{"null method", `{"client_id":"c","token_endpoint_auth_method":null}`, "none"},
		{"empty secret", `{"client_id":"c","client_secret":""}`, "none"},
		{"named method", `{"client_id":"c","token_endpoint_auth_method":"client_secret_post"}`, ""},
		{"secret", `{"client_id":"c","client_secret":"s"}`, ""},
		{"odd method type", `{"client_id":"c","token_endpoint_auth_method":1}`, ""},
		{"not an object", `["c"]`, ""},
		{"not JSON", `client_id=c`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := publicClientDefault([]byte(tc.body))
			if tc.want == "" {
				if string(got) != tc.body {
					t.Fatalf("changed: %s", got)
				}
				return
			}
			var m struct {
				ClientID string `json:"client_id"`
				Method   string `json:"token_endpoint_auth_method"`
			}
			if err := json.Unmarshal(got, &m); err != nil || m.ClientID != "c" || m.Method != tc.want {
				t.Fatalf("%s: %v", got, err)
			}
		})
	}
}
