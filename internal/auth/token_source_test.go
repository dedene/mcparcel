package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

// tokenDir is a private directory on a path OpenTokenFile accepts.
func tokenDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(config.DefaultTempDir(), "mcp-token-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeTokenFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestServiceAccountTokenEnv(t *testing.T) {
	p := config.Profile{Mode: config.ProfileModeServiceAccount, TokenEnv: "OP_SERVICE_ACCOUNT_TOKEN"}
	for _, c := range []struct {
		env  map[string]string
		want string
		err  error
	}{
		{map[string]string{"OP_SERVICE_ACCOUNT_TOKEN": "ops_fixture"}, "ops_fixture", nil},
		{map[string]string{"OP_SERVICE_ACCOUNT_TOKEN": "ops_fixture\n"}, "ops_fixture", nil},
		{map[string]string{"OTHER": "ops_fixture"}, "", ErrTokenUnavailable},
		{nil, "", ErrTokenUnavailable},
		{map[string]string{"OP_SERVICE_ACCOUNT_TOKEN": ""}, "", ErrTokenUnavailable},
		{map[string]string{"OP_SERVICE_ACCOUNT_TOKEN": " \n"}, "", ErrTokenUnavailable},
	} {
		got, err := ServiceAccountToken(p, c.env)
		if got != c.want || err != c.err {
			t.Fatalf("env %v: got %q, %v", c.env, got, err)
		}
	}
	if _, err := ServiceAccountToken(config.Profile{Mode: config.ProfileModeServiceAccount}, map[string]string{"": "x"}); err != ErrTokenUnavailable {
		t.Fatal("profile without a source", err)
	}
}

func TestServiceAccountTokenFile(t *testing.T) {
	dir := tokenDir(t)
	path := filepath.Join(dir, "op-token")
	p := config.Profile{Mode: config.ProfileModeServiceAccount, TokenFile: path}
	for _, c := range []struct {
		content string
		mode    os.FileMode
		want    string
		err     error
	}{
		{"ops_fixture\n", 0o600, "ops_fixture", nil},
		{"ops_fixture\r\n", 0o400, "ops_fixture", nil},
		{"  ops_fixture  ", 0o600, "ops_fixture", nil},
		{strings.Repeat("a", config.MaxTokenFileBytes), 0o600, strings.Repeat("a", config.MaxTokenFileBytes), nil},
		{"ops fixture", 0o600, "", ErrTokenUnavailable},
		{"ops_\x00fixture", 0o600, "", ErrTokenUnavailable},
		{"ops_fixture\nsecond", 0o600, "", ErrTokenUnavailable},
		{"ops_fixturé", 0o600, "", ErrTokenUnavailable},
		{"", 0o600, "", ErrTokenUnavailable},
		{"\n\t ", 0o600, "", ErrTokenUnavailable},
		{strings.Repeat("a", config.MaxTokenFileBytes+1), 0o600, "", ErrTokenUnavailable},
		{strings.Repeat("a", config.MaxTokenFileBytes) + "\n", 0o600, "", ErrTokenUnavailable},
		{"ops_fixture\n", 0o644, "", ErrTokenUnsafe},
		{"ops_fixture\n", 0o602, "", ErrTokenUnsafe},
	} {
		writeTokenFile(t, path, c.content, c.mode)
		got, err := ServiceAccountToken(p, nil)
		if got != c.want || err != c.err {
			t.Fatalf("content %.20q mode %o: got %.20q, %v", c.content, c.mode, got, err)
		}
	}
	// The file is read again on every call, so a rotated token applies at once.
	writeTokenFile(t, path, "ops_rotated\n", 0o600)
	if got, err := ServiceAccountToken(p, nil); got != "ops_rotated" || err != nil {
		t.Fatal("rotated token", got, err)
	}
	if _, err := ServiceAccountToken(config.Profile{Mode: config.ProfileModeServiceAccount, TokenFile: filepath.Join(dir, "absent")}, nil); err != ErrTokenUnavailable {
		t.Fatal("missing file", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "absent"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := ServiceAccountToken(config.Profile{Mode: config.ProfileModeServiceAccount, TokenFile: link}, nil); err != ErrTokenUnavailable {
		t.Fatal("dangling link", err)
	}
}

func TestServiceAccountTokenOpenErrors(t *testing.T) {
	original := openTokenFile
	t.Cleanup(func() { openTokenFile = original })
	p := config.Profile{Mode: config.ProfileModeServiceAccount, TokenFile: "/run/secrets/op-token"}
	for _, c := range []struct {
		open error
		want error
	}{
		{os.ErrPermission, ErrTokenUnavailable},
		{os.ErrNotExist, ErrTokenUnavailable},
		{config.ErrUnsafePath, ErrTokenUnsafe},
		{errors.New("unexpected /run/secrets/op-token"), ErrTokenUnavailable},
	} {
		openTokenFile = func(string) (*os.File, error) { return nil, c.open }
		if _, err := ServiceAccountToken(p, nil); err != c.want {
			t.Fatalf("open %v: got %v", c.open, err)
		}
	}
}

func TestTokenSentinelsSurviveSafeProviderError(t *testing.T) {
	for _, sentinel := range []error{ErrTokenUnavailable, ErrTokenUnsafe} {
		if got := safeProviderError(t.Context(), sentinel); got != sentinel {
			t.Fatal("sentinel collapsed", sentinel, got)
		}
	}
}
