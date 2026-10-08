//go:build mcparceltest

package auth

import (
	"context"

	"github.com/dedene/mcparcel/internal/config"
)

// NewFixtureTokenProvider is the real 1Password provider for service-account
// profiles, token read and rejected-token backoff included, over a fixture
// client constructor, so black-box tests exercise the backoff end to end.
// Desktop modes get ErrProvider.
func NewFixtureTokenProvider(env map[string]string, service func(ctx context.Context, token string) (SecretClient, error)) Provider {
	return newOnePasswordProvider("fixture", nil, func(ctx context.Context, token, _ string) (SecretClient, error) {
		return service(ctx, token)
	}, func(profile config.Profile) (string, error) { return ServiceAccountToken(profile, env) })
}
