package config

import (
	"cmp"
	"errors"
	"regexp"
	"strings"
)

// semverPattern is the Semantic Versioning 2.0.0 grammar: core, prerelease
// (group 4) and build metadata (group 5).
var semverPattern = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
	`(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?` +
	`(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// ErrCatalogNewer is catalog_requires_upgrade: a catalog's minVersion is above
// the running version.
var ErrCatalogNewer = errors.New("catalog requires a newer version")

// CatalogNewerError names both versions; it matches ErrCatalogNewer.
type CatalogNewerError struct{ Min, Current string }

func (e *CatalogNewerError) Error() string {
	return "catalog requires " + e.Min + " or newer; this is " + e.Current
}
func (e *CatalogNewerError) Is(target error) bool { return target == ErrCatalogNewer }

// validMinVersion reports whether v is semver without build metadata.
func validMinVersion(v string) bool {
	m := semverPattern.FindStringSubmatch(v)
	return m != nil && m[5] == "" && !strings.Contains(v, "+")
}

// CompareVersion orders two semver strings by precedence, ignoring build
// metadata; ok is false when either is not semver (dev builds, git describe).
func CompareVersion(a, b string) (order int, ok bool) {
	ma, mb := semverPattern.FindStringSubmatch(a), semverPattern.FindStringSubmatch(b)
	if ma == nil || mb == nil {
		return 0, false
	}
	for i := 1; i <= 3; i++ {
		if c := compareNumeric(ma[i], mb[i]); c != 0 {
			return c, true
		}
	}
	return comparePrerelease(ma[4], mb[4]), true
}

// CheckMinVersion returns a *CatalogNewerError when current is below
// cat.MinVersion; nil when there is none or current is not semver.
func CheckMinVersion(cat Catalog, current string) error {
	if cat.MinVersion == "" {
		return nil
	}
	if c, ok := CompareVersion(current, cat.MinVersion); ok && c < 0 {
		return &CatalogNewerError{Min: cat.MinVersion, Current: current}
	}
	return nil
}

// compareNumeric compares digit strings without leading zeros of any length.
func compareNumeric(a, b string) int {
	if c := cmp.Compare(len(a), len(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1 // a release ranks above its prereleases
	case b == "":
		return -1
	}
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range min(len(pa), len(pb)) {
		na, nb := numeric(pa[i]), numeric(pb[i])
		var c int
		switch {
		case na && nb:
			c = compareNumeric(pa[i], pb[i])
		case na:
			c = -1 // numeric identifiers rank below alphanumeric ones
		case nb:
			c = 1
		default:
			c = strings.Compare(pa[i], pb[i])
		}
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(pa), len(pb))
}

func numeric(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) < 0
}
