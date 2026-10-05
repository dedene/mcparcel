//go:build !mcparceltest

package cmd

import (
	"context"
	"errors"
	"net/url"
	"os/exec"
	"time"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

func newCredentials(_ config.Paths, version string) auth.Resolver {
	return auth.NewResolver(auth.ResolverOptions{Provider: auth.NewOnePasswordProvider(version)})
}

func newKeychain(config.Paths) func(context.Context, string) (string, error) {
	return runtimeclient.KeychainLookup
}

func newKeyring(config.Paths) auth.Keyring { return auth.SystemKeyring{} }

// newBrowser opens an https URL, or an http URL on a loopback host, in the
// default browser. The URL is one argv entry; no shell runs.
func newBrowser(config.Paths) func(context.Context, string) error {
	return func(ctx context.Context, raw string) error {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.Scheme != "https" && (u.Scheme != "http" || !loopbackHost(u.Hostname())) {
			return errors.New("unsupported sign-in URL")
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "/usr/bin/open", raw).Run()
	}
}

func loopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}
