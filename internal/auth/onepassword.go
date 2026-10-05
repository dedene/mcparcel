package auth

import (
	"context"
	"runtime"
	"sync"

	"github.com/1password/onepassword-sdk-go"

	"github.com/dedene/mcparcel/internal/config"
)

type onePasswordProvider struct {
	accountMu        sync.Mutex
	account          string
	accountSet       bool
	version          string
	desktop, service func(context.Context, string, string) (SecretClient, error)
}
type safeSecretClient struct{ client SecretClient }

func (c safeSecretClient) Resolve(ctx context.Context, ref string) (string, error) {
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	v, err := c.client.Resolve(ctx, ref)
	if err != nil {
		return "", safeProviderError(ctx, err)
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return v, nil
}

func newOnePasswordProvider(version string, desktop func(context.Context, string, string) (SecretClient, error), service func(context.Context, string, string) (SecretClient, error)) Provider {
	return &onePasswordProvider{version: version, desktop: desktop, service: service}
}

func NewOnePasswordProvider(version string) Provider {
	return newOnePasswordProvider(version, func(ctx context.Context, account, version string) (SecretClient, error) {
		c, err := constructSDKClient(func() (*onepassword.Client, error) {
			return onepassword.NewClient(ctx, onepassword.WithDesktopAppIntegration(account), onepassword.WithIntegrationInfo("MCParcel", version))
		})
		if err != nil {
			return nil, safeProviderError(ctx, err)
		}
		return sdkSecrets(c), nil
	}, func(ctx context.Context, token, version string) (SecretClient, error) {
		c, err := constructSDKClient(func() (*onepassword.Client, error) {
			return onepassword.NewClient(ctx, onepassword.WithServiceAccountToken(token), onepassword.WithIntegrationInfo("MCParcel", version))
		})
		if err != nil {
			return nil, safeProviderError(ctx, err)
		}
		return sdkSecrets(c), nil
	})
}

func (p *onePasswordProvider) Bootstrap(ctx context.Context, profile config.Profile) (SecretClient, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	p.accountMu.Lock()
	if p.accountSet && p.account != profile.Account {
		p.accountMu.Unlock()
		return nil, ErrAccountConflict
	}
	p.account, p.accountSet = profile.Account, true
	p.accountMu.Unlock()
	desktop, err := p.desktop(ctx, profile.Account, p.version)
	if err != nil {
		return nil, safeProviderError(ctx, err)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if desktop == nil {
		return nil, ErrProvider
	}
	token, err := desktop.Resolve(ctx, profile.BootstrapRef)
	desktop = nil
	if err != nil {
		return nil, safeProviderError(ctx, err)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	service, err := p.service(ctx, token, p.version)
	token = ""
	if err != nil {
		return nil, safeProviderError(ctx, err)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if service == nil {
		return nil, ErrProvider
	}
	return safeSecretClient{client: service}, nil
}

type sdkSecretClient struct {
	owner   *onepassword.Client
	secrets SecretClient
}

func sdkSecrets(c *onepassword.Client) SecretClient {
	return sdkSecretClient{owner: c, secrets: c.Secrets()}
}

func (c sdkSecretClient) Resolve(ctx context.Context, ref string) (string, error) {
	value, err := c.secrets.Resolve(ctx, ref)
	runtime.KeepAlive(c.owner)
	return value, err
}

var sdkConstructionMu sync.Mutex

func constructSDKClient(create func() (*onepassword.Client, error)) (*onepassword.Client, error) {
	sdkConstructionMu.Lock()
	defer sdkConstructionMu.Unlock()
	return create()
}
