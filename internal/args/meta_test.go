package args

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseMeta(t *testing.T) {
	m, err := ParseMeta([]byte(`{"x-codex-turn-metadata":{"session_id":"s","turn_id":"t","n":9007199254740993},"com.example/k":1}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(m)
	if !strings.Contains(string(b), `"n":9007199254740993`) || len(m) != 2 {
		t.Fatal(string(b))
	}
	big := `{"k":"` + strings.Repeat("a", MaxMetaBytes) + `"}`
	for _, in := range []string{``, `{`, `[]`, `"x"`, `null`, `{"a":1,"a":2}`, `{"progressToken":1}`, `{"io.modelcontextprotocol/clientInfo":{}}`, `{"dev.mcp/x":1}`, `{"x":1} {}`, big} {
		if _, err := ParseMeta([]byte(in)); !errors.Is(err, ErrInvalidArgs) {
			t.Fatalf("%.40q: %v", in, err)
		}
	}
}
