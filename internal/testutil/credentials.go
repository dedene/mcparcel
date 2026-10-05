package testutil

import (
	"context"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
)

type FakeProvider struct {
	BootstrapFunc func(context.Context, config.Profile) (auth.SecretClient, error)
}

func (p FakeProvider) Bootstrap(ctx context.Context, profile config.Profile) (auth.SecretClient, error) {
	if p.BootstrapFunc == nil {
		return nil, auth.ErrProvider
	}
	return p.BootstrapFunc(ctx, profile)
}

type FakeSecretClient struct {
	ResolveFunc func(context.Context, string) (string, error)
}

func (c FakeSecretClient) Resolve(ctx context.Context, ref string) (string, error) {
	if c.ResolveFunc == nil {
		return "", auth.ErrProvider
	}
	return c.ResolveFunc(ctx, ref)
}
