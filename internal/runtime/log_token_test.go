package runtime

import (
	"bytes"
	"testing"
)

func TestWriteTokenEvent(t *testing.T) {
	var b bytes.Buffer
	for _, tc := range []struct {
		event  string
		fields map[string]any
	}{
		{"oauth_token_minted", map[string]any{"trigger": "first", "ttl": int64(900)}},
		{"oauth_token_minted", map[string]any{"trigger": "401", "ttl": int64(0)}},
		{"oauth_token_mint_failed", map[string]any{"code": "invalid_client", "status": 401}},
		{"oauth_token_rejected", map[string]any{"code": "http_403", "status": 403}},
	} {
		if e := WriteTokenEvent(&b, tc.event, tc.fields); e != nil {
			t.Fatal(tc, e)
		}
	}
	want := `{"event":"oauth_token_minted","trigger":"first","ttl":900}
{"event":"oauth_token_minted","trigger":"401","ttl":0}
{"event":"oauth_token_mint_failed","code":"invalid_client","status":401}
{"event":"oauth_token_rejected","code":"http_403","status":403}
`
	if b.String() != want {
		t.Fatal(b.String())
	}
	b.Reset()
	for _, tc := range []struct {
		event  string
		fields map[string]any
	}{
		{"oauth_token_minted", map[string]any{"trigger": "manual", "ttl": int64(1)}},
		{"oauth_token_minted", map[string]any{"trigger": "first", "ttl": int64(1), "url": "https://front.example"}},
		{"oauth_token_mint_failed", map[string]any{"code": "Bad Secret s3cr3t", "status": 401}},
		{"oauth_token_mint_failed", map[string]any{"code": "invalid_client"}},
		{"client_credentials_token_minted", nil},
	} {
		if WriteTokenEvent(&b, tc.event, tc.fields) == nil {
			t.Fatal("accepted", tc)
		}
	}
	if b.Len() != 0 {
		t.Fatal(b.String())
	}
}
