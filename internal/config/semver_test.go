package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

func TestCompareVersion(t *testing.T) {
	// Semantic Versioning 2.0.0, section 11, in ascending order.
	ordered := []string{"0.9.9", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.2.0", "1.10.0", "2.0.0", "18446744073709551616.0.0"}
	for i := range ordered {
		for j := range ordered {
			got, ok := config.CompareVersion(ordered[i], ordered[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if !ok || got != want {
				t.Fatalf("%s vs %s: %d %v, want %d", ordered[i], ordered[j], got, ok, want)
			}
		}
	}
	// Build metadata does not take part in precedence.
	for _, pair := range [][2]string{{"0.1.0-rc.1+3f2a9c1d0e4b", "0.1.0-rc.1"}, {"1.0.0+a", "1.0.0+b"}} {
		if got, ok := config.CompareVersion(pair[0], pair[1]); !ok || got != 0 {
			t.Fatal(pair, got, ok)
		}
	}
	if got, ok := config.CompareVersion("0.1.0-rc.1+abc", "0.1.0"); !ok || got != -1 {
		t.Fatal("a release candidate ranks below its release", got, ok)
	}
	for _, bad := range []string{"dev", "5319954-dirty", "v1.0.0", "1.0", "01.0.0", "1.0.0-01", "1.0.0-", "1.0.0+", ""} {
		if _, ok := config.CompareVersion(bad, "1.0.0"); ok {
			t.Fatalf("%q compared", bad)
		}
		if _, ok := config.CompareVersion("1.0.0", bad); ok {
			t.Fatalf("%q compared", bad)
		}
	}
}

func TestCheckMinVersion(t *testing.T) {
	cat := config.Catalog{MinVersion: "0.2.0"}
	err := config.CheckMinVersion(cat, "0.1.0-rc.1+abc")
	var newer *config.CatalogNewerError
	if !errors.Is(err, config.ErrCatalogNewer) || !errors.As(err, &newer) || newer.Min != "0.2.0" || newer.Current != "0.1.0-rc.1+abc" {
		t.Fatal(err)
	}
	for _, current := range []string{"0.2.0", "0.2.0+abc", "1.0.0", "dev", "5319954-dirty"} {
		if err := config.CheckMinVersion(cat, current); err != nil {
			t.Fatal(current, err)
		}
	}
	if err := config.CheckMinVersion(config.Catalog{}, "0.0.1"); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogMinVersionDecode(t *testing.T) {
	c, err := config.DecodeCatalog([]byte(`{"schemaVersion":1,"minVersion":"0.2.0-rc.1","connections":{}}`))
	if err != nil || c.MinVersion != "0.2.0-rc.1" {
		t.Fatal(c.MinVersion, err)
	}
	for _, bad := range []string{`"0.2.0+abc"`, `"v0.2.0"`, `"0.2"`, `""`, `2`} {
		_, err := config.DecodeCatalog([]byte(`{"schemaVersion":1,"minVersion":` + bad + `,"connections":{}}`))
		if bad == `""` {
			// Empty is the same as absent.
			if err != nil {
				t.Fatal(bad, err)
			}
			continue
		}
		if !errors.Is(err, config.ErrConfig) {
			t.Fatal(bad, err)
		}
	}
	if _, err := config.DecodeCatalog([]byte(`{"schemaVersion":1,"minVersion":"v1","connections":{}}`)); !strings.Contains(config.FieldReason(err), "minVersion") {
		t.Fatal(config.FieldReason(err))
	}
}

func TestPersonalRejectsMinVersion(t *testing.T) {
	personal, err := config.DecodePersonal([]byte(`{"schemaVersion":1,"minVersion":"0.1.0","connections":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	state := config.State{Personal: personal, Local: config.Local{SchemaVersion: 1}, Selections: config.Selections{SchemaVersion: 1, Connections: map[string]config.Selection{}}, Catalogs: map[string]config.Catalog{}}
	err = config.ValidateState(state)
	if !errors.Is(err, config.ErrConfig) || config.FieldReason(err) != "personal.minVersion: allowed only in a GitHub catalog" {
		t.Fatal(err, config.FieldReason(err))
	}
}
