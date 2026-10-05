//go:build !mcparceltest

package cmd

import (
	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
)

func newCredentials(_ config.Paths, version string) auth.Resolver {
	return auth.NewResolver(auth.ResolverOptions{Provider: auth.NewOnePasswordProvider(version)})
}
