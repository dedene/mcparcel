//go:build mcparceltest

package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

// desktopSupported is true in fixture builds so the black-box desktop
// scenarios also run on Linux; production Linux builds run headless only.
func desktopSupported() bool { return true }

func newCredentials(paths config.Paths, _ string, env map[string]string) auth.Resolver {
	var mu sync.Mutex
	record := func(event string) error {
		mu.Lock()
		defer mu.Unlock()
		dir, err := config.OpenPrivateDirUnder(paths.StateRoot, paths.StateDir, true)
		if err != nil {
			return auth.ErrProvider
		}
		defer dir.Close()
		file, err := config.OpenPrivateFile(dir, "fixture-auth-events", true)
		if err != nil {
			return auth.ErrProvider
		}
		defer file.Close()
		if _, err = file.Seek(0, 2); err != nil {
			return auth.ErrProvider
		}
		if _, err = file.WriteString(event + "\n"); err != nil {
			return auth.ErrProvider
		}
		return nil
	}
	present := func(name string) bool {
		_, err := os.Stat(filepath.Join(paths.StateDir, name))
		return err == nil
	}
	// A desktop profile reads through the desktop client itself and never
	// reads a bootstrap reference. A service-account profile reads its token
	// as production does; only FIXTURE-SA-TOKEN is accepted.
	provider := testutil.FakeProvider{BootstrapFunc: func(ctx context.Context, profile config.Profile) (auth.SecretClient, error) {
		event := "bootstrap"
		switch {
		case profile.PromptFree():
			token, err := auth.ServiceAccountToken(profile, env)
			if err != nil {
				return nil, err
			}
			if token != "FIXTURE-SA-TOKEN" {
				return nil, auth.ErrProvider
			}
			event = "bootstrap-token"
		case profile.Account != "Fixture account":
			return nil, auth.ErrProvider
		case profile.Mode == "desktop" && profile.BootstrapRef == "":
			event = "bootstrap-desktop"
		case profile.BootstrapRef != "op://Private/fixture/token":
			return nil, auth.ErrProvider
		}
		if err := record(event); err != nil {
			return nil, err
		}
		if _, err := os.Stat(paths.StateDir + "/fixture-bootstrap-block"); err == nil {
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				if _, err := os.Stat(paths.StateDir + "/fixture-bootstrap-release"); err == nil {
					break
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-ticker.C:
				}
			}
		}
		// This fictional bootstrap value is never installed in a lease or child environment.
		const bootstrapToken = "FIXTURE-BOOTSTRAP-TOKEN"
		return testutil.FakeSecretClient{ResolveFunc: func(ctx context.Context, ref string) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			// StateDir/fixture-revoked and fixture-rate-limit fail every read
			// as a revoked service account or a rate limit would.
			if present("fixture-revoked") {
				return "", fmt.Errorf("%w: %s revoked", auth.ErrProvider, bootstrapToken)
			}
			if present("fixture-rate-limit") {
				return "", fmt.Errorf("%w: %s", auth.ErrRateLimited, bootstrapToken)
			}
			event, value := "", ""
			switch ref {
			case "op://Fixture/api/key":
				event, value = "resolve-api", "FIXTURE-API-KEY"
				// StateDir/fixture-api-value replaces the value, as an edited
				// 1Password item would.
				if b, err := os.ReadFile(filepath.Join(paths.StateDir, "fixture-api-value")); err == nil {
					value = strings.TrimSuffix(string(b), "\n")
				}
			case "op://Fixture/other/key":
				event, value = "resolve-other", "FIXTURE-OTHER-KEY"
			default:
				return "", fmt.Errorf("%w: %s rejected %s", auth.ErrProvider, bootstrapToken, ref)
			}
			if err := record(event); err != nil {
				return "", err
			}
			return value, nil
		}}, nil
	}}
	return auth.NewResolver(auth.ResolverOptions{Provider: provider})
}

// newKeychain never runs security: test binaries read a fixture file instead.
func newKeychain(paths config.Paths) func(context.Context, string) (string, error) {
	return func(_ context.Context, name string) (string, error) {
		if name == "" || strings.ContainsAny(name, "/.") {
			return "", auth.ErrProvider
		}
		b, err := os.ReadFile(filepath.Join(paths.StateDir, "fixture-keychain-"+name))
		if err != nil {
			return "", auth.ErrProvider
		}
		return strings.TrimSuffix(string(b), "\n"), nil
	}
}

// fixtureKeyring stores each item in a private file under StateDir and never
// runs security. StateDir/fixture-keyring-unavailable fails every call.
type fixtureKeyring struct {
	mu    sync.Mutex
	paths config.Paths
}

func newKeyring(paths config.Paths) auth.Keyring { return &fixtureKeyring{paths: paths} }

var errFixtureKeyring = errors.New("fixture keyring unavailable")

// open returns the StateDir handle and the item's file name.
func (k *fixtureKeyring) open(service, account string, create bool) (*os.File, string, error) {
	if _, err := os.Stat(filepath.Join(k.paths.StateDir, "fixture-keyring-unavailable")); err == nil {
		return nil, "", errFixtureKeyring
	}
	dir, err := config.OpenPrivateDirUnder(k.paths.StateRoot, k.paths.StateDir, create)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", auth.ErrNoSession
	}
	if err != nil {
		return nil, "", errFixtureKeyring
	}
	sum := sha256.Sum256([]byte(service + "\x00" + account))
	return dir, "fixture-keyring-" + hex.EncodeToString(sum[:]), nil
}

func (k *fixtureKeyring) Get(service, account string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	dir, name, err := k.open(service, account, false)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	f, err := config.OpenPrivateFile(dir, name, false)
	if errors.Is(err, os.ErrNotExist) {
		return "", auth.ErrNoSession
	}
	if err != nil {
		return "", errFixtureKeyring
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return "", errFixtureKeyring
	}
	return string(b), nil
}

func (k *fixtureKeyring) Set(service, account, secret string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	dir, name, err := k.open(service, account, true)
	if err != nil {
		return err
	}
	defer dir.Close()
	f, err := config.OpenPrivateFile(dir, name, true)
	if err != nil {
		return errFixtureKeyring
	}
	defer f.Close()
	if f.Truncate(0) != nil {
		return errFixtureKeyring
	}
	if _, err = f.WriteString(secret); err != nil {
		return errFixtureKeyring
	}
	return nil
}

func (k *fixtureKeyring) Delete(service, account string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	dir, name, err := k.open(service, account, false)
	if err != nil {
		return err
	}
	_ = dir.Close()
	err = os.Remove(filepath.Join(k.paths.StateDir, name))
	if errors.Is(err, os.ErrNotExist) {
		return auth.ErrNoSession
	}
	if err != nil {
		return errFixtureKeyring
	}
	return nil
}

// browserOpens is true: the fixture browser below follows the URL.
const browserOpens = true

// newBrowser never opens a browser: it follows the sign-in URL to the
// daemon's callback and keeps the page in StateDir/fixture-browser-page.
// StateDir/fixture-browser-off turns it off.
func newBrowser(paths config.Paths) func(context.Context, string) error {
	return func(ctx context.Context, raw string) error {
		if _, err := os.Stat(filepath.Join(paths.StateDir, "fixture-browser-off")); err == nil {
			return nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return err
		}
		resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return err
		}
		dir, err := config.OpenPrivateDirUnder(paths.StateRoot, paths.StateDir, true)
		if err != nil {
			return err
		}
		defer dir.Close()
		f, err := config.OpenPrivateFile(dir, "fixture-browser-page", true)
		if err != nil {
			return err
		}
		defer f.Close()
		if err = f.Truncate(0); err != nil {
			return err
		}
		_, err = f.Write(body)
		return err
	}
}

// newTerminal never reads a terminal: StateDir/fixture-terminal holds the
// typed lines, read once and shared by the process's prompts. Absent means
// no terminal.
func newTerminal(paths config.Paths, _, _ *os.File) func(context.Context) io.Reader {
	b, err := os.ReadFile(filepath.Join(paths.StateDir, "fixture-terminal"))
	if err != nil {
		return nil
	}
	typed := strings.NewReader(string(b))
	return func(context.Context) io.Reader { return typed }
}

// newSetupTerminal is always false: fixture binaries never start the setup UI.
func newSetupTerminal(_, _ *os.File) bool { return false }

// newDialog never runs osascript: it keeps argv in StateDir/fixture-dialog-args
// and returns StateDir/fixture-dialog-answer (absent: no button).
func newDialog(paths config.Paths) func(context.Context, []string) (string, error) {
	return func(_ context.Context, argv []string) (string, error) {
		b, err := json.Marshal(argv)
		if err != nil {
			return "", err
		}
		if err = os.WriteFile(filepath.Join(paths.StateDir, "fixture-dialog-args"), b, 0o600); err != nil {
			return "", err
		}
		b, err = os.ReadFile(filepath.Join(paths.StateDir, "fixture-dialog-answer"))
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return string(b), err
	}
}

// onePasswordAppDirs never points at the real /Applications: black-box tests
// put a 1Password.app directory in StateDir/fixture-apps when they need one.
func onePasswordAppDirs(paths config.Paths) []string {
	return []string{filepath.Join(paths.StateDir, "fixture-apps")}
}

// retainEnabled is true only when StateDir/fixture-retain exists, so the
// race-built test binaries are not copied by every test that starts a daemon.
func retainEnabled(paths config.Paths) bool {
	_, err := os.Stat(filepath.Join(paths.StateDir, "fixture-retain"))
	return err == nil
}
