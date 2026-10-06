package auth

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func render(t *testing.T, p callbackPage) string {
	t.Helper()
	var b bytes.Buffer
	if err := renderCallback(&b, p); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestCallbackGolden(t *testing.T) {
	for file, p := range map[string]callbackPage{
		"signed-in": {State: "signed_in", Label: "Linear", Name: "linear"},
		"failed":    {State: "failed", Label: "Linear", Name: "linear", Code: "access_denied", Detail: "The user denied the request."},
		"expired":   {State: "expired", Label: "Linear", Name: "linear"},
		"mismatch":  {State: "mismatch", Label: "Linear", Name: "linear"},
	} {
		want, err := os.ReadFile(filepath.Join("..", "..", "testdata", "oauth", "signin", file+".html"))
		if err != nil {
			t.Fatal(err)
		}
		if got := render(t, p); strings.TrimSpace(got) != strings.TrimSpace(string(want)) {
			t.Fatalf("%s differs from the approved mockup:\n%s", file, got)
		}
	}
	failed := render(t, callbackPage{State: "failed", Label: "Linear", Name: "linear"})
	if strings.Contains(failed, "<dl>") {
		t.Fatal("empty provider error rendered a dl")
	}
	if got := render(t, callbackPage{State: "failed", Label: "Linear", Name: "linear", Detail: "d"}); strings.Contains(got, "Provider error") || !strings.Contains(got, "<dt>Detail</dt><dd>d</dd>") {
		t.Fatal(got)
	}
	if err := renderCallback(&bytes.Buffer{}, callbackPage{State: "unknown"}); err == nil {
		t.Fatal("unknown state rendered")
	}
}

func TestCallbackEscapesProviderError(t *testing.T) {
	got := render(t, callbackPage{
		State: "failed", Label: `<b>Lin</b>`, Name: "lin<ear",
		Code:   "<script>alert(1)</script>" + strings.Repeat("c", 100),
		Detail: "a\x00b\x1b[31m<img onerror=x> " + strings.Repeat("d", 400),
	})
	for _, bad := range []string{"<script>", "<img", "<b>", "lin<ear", "\x00", "\x1b", strings.Repeat("c", 60), strings.Repeat("d", 300)} {
		if strings.Contains(got, bad) {
			t.Fatalf("rendered %q", bad)
		}
	}
	for _, good := range []string{"&lt;b&gt;Lin&lt;/b&gt; did not sign you in", "&lt;img onerror=x&gt;", "ab[31m"} {
		if !strings.Contains(got, good) {
			t.Fatalf("missing %q in %s", good, got)
		}
	}
}

// TestCallbackDarkTheme pins what a dark-mode browser needs: no data-theme on
// <html> (which would switch the dark rules off), a dark block keyed on
// prefers-color-scheme alone, and a color-scheme declaration so the browser
// also draws its own parts (canvas, scrollbars) dark.
func TestCallbackDarkTheme(t *testing.T) {
	for _, state := range []string{"signed_in", "failed", "expired", "mismatch"} {
		got := render(t, callbackPage{State: state, Label: "Linear", Name: "linear"})
		html, _, _ := strings.Cut(got, ">")
		if html != `<!doctype html` {
			t.Fatalf("%s: unexpected prologue %q", state, html)
		}
		rest := got[len(html)+1:]
		if tag, _, _ := strings.Cut(rest, ">"); tag != `<html lang="en"` {
			t.Fatalf("%s: <html> carries attributes beyond lang: %q", state, tag)
		}
		for _, want := range []string{
			`<meta name="color-scheme" content="light dark">`,
			`:root{color-scheme:light dark;--bg:#F4F4EC;`,
			`@media (prefers-color-scheme:dark){:root:not([data-theme=light]){--bg:#1B201B;`,
			`:root[data-theme=light]{color-scheme:light}`,
			`:root[data-theme=dark]{color-scheme:dark;--bg:#1B201B;`,
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("%s: missing %q", state, want)
			}
		}
		if strings.Contains(got, "data-theme=\"") || strings.Contains(got, "data-theme='") {
			t.Fatalf("%s: page hard-codes a theme", state)
		}
	}
}

func TestCallbackNoExternalRequests(t *testing.T) {
	for _, state := range []string{"signed_in", "failed", "expired", "mismatch"} {
		got := render(t, callbackPage{State: state, Label: "Linear", Name: "linear", Code: "access_denied", Detail: "see https://x.invalid"})
		got = strings.ReplaceAll(got, "see https://x.invalid", "")
		for _, bad := range []string{"://", "src=", "url(", "@import", "<script", "<link"} {
			if strings.Contains(got, bad) {
				t.Fatalf("%s contains %q", state, bad)
			}
		}
	}
}
