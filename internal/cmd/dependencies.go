//go:build !mcparceltest

package cmd

import (
	"context"

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
