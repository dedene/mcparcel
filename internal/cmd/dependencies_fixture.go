//go:build mcparceltest

package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/testutil"
)

func newCredentials(paths config.Paths, _ string) auth.Resolver {
	var mu sync.Mutex
	record := func(event string) error {
		mu.Lock()
		defer mu.Unlock()
		dir, err := config.OpenPrivateDir(paths.StateDir, true)
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
	provider := testutil.FakeProvider{BootstrapFunc: func(ctx context.Context, profile config.Profile) (auth.SecretClient, error) {
		if profile.Account != "Fixture account" || profile.BootstrapRef != "op://Private/fixture/token" {
			return nil, auth.ErrProvider
		}
		if err := record("bootstrap"); err != nil {
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
			event, value := "", ""
			switch ref {
			case "op://Fixture/api/key":
				event, value = "resolve-api", "FIXTURE-API-KEY"
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
