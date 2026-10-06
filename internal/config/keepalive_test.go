package config

import (
	"errors"
	"testing"
)

func keepAlivePersonal(value string) []byte {
	return []byte(`{"schemaVersion":1,"connections":{"fixture":{"transport":{"type":"http","url":"https://fixture.invalid/mcp"},"lifecycle":{"keepAlive":` + value + `}}}}`)
}

func TestLifecycleKeepAliveValidation(t *testing.T) {
	for _, ok := range []string{`"off"`, `"12h"`, `"1h"`, `"168h"`} {
		p, err := DecodePersonal(keepAlivePersonal(ok))
		if err != nil || p.Connections["fixture"].Lifecycle == nil || `"`+p.Connections["fixture"].Lifecycle.KeepAlive+`"` != ok {
			t.Fatal(ok, p, err)
		}
	}
	for _, bad := range []string{`""`, `"0s"`, `"30m"`, `"-1h"`, `"on"`, `false`, `null`} {
		if _, err := DecodePersonal(keepAlivePersonal(bad)); !errors.Is(err, ErrConfig) {
			t.Fatal(bad, err)
		}
	}
}

// The keep-alive interval changes neither the session's identity nor whether
// the connection needs review, so editing it never reconnects or asks.
func TestKeepAliveNotInConnectionHash(t *testing.T) {
	load := func(value string) Snapshot {
		t.Helper()
		p, err := DecodePersonal(keepAlivePersonal(value))
		if err != nil {
			t.Fatal(err)
		}
		return Snapshot{Personal: p, Local: Local{SchemaVersion: 1}}
	}
	base, err := load(`"off"`).ConnectionHash("fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{`"12h"`, `"48h"`} {
		got, err := load(v).ConnectionHash("fixture")
		if err != nil || got != base {
			t.Fatal(v, got, base, err)
		}
	}
	a := load(`"off"`).Personal.Connections["fixture"]
	b := load(`"12h"`).Personal.Connections["fixture"]
	if ExecutionChanged(a, b) || ExecutionChanged(b, a) {
		t.Fatal("keepAlive counted as an execution change")
	}
	b.Lifecycle = nil
	if ExecutionChanged(a, b) {
		t.Fatal("removing keepAlive counted as an execution change")
	}
}
