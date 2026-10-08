package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"runtime"
	"sync"
	"time"

	"github.com/1password/onepassword-sdk-go"

	"github.com/dedene/mcparcel/internal/config"
)

// OnePasswordOptions configures NewOnePasswordProvider.
type OnePasswordOptions struct {
	// DesktopApp wires the 1Password desktop-app integration. Without it a
	// desktop or desktop-service-account profile gets ErrProvider.
	DesktopApp bool
	// Env is the environment a service-account profile's tokenEnv is read
	// from: the daemon's login environment, the same map env: references use.
	Env map[string]string
	// Now is the clock of the rejected-token backoff; default time.Now.
	Now func() time.Time
}

// clientFactory builds a 1Password client from an account name (desktop) or a
// service-account token, plus the integration version.
type clientFactory func(context.Context, string, string) (SecretClient, error)

type onePasswordProvider struct {
	accountMu        sync.Mutex
	account          string
	accountSet       bool
	version          string
	desktop, service clientFactory
	token            func(config.Profile) (string, error)
	now              func() time.Time
	failures         bootstrapFailures
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

// newOnePasswordProvider is the test seam. A nil desktop refuses the desktop
// modes; token reads a service-account profile's token.
func newOnePasswordProvider(version string, desktop, service clientFactory, token func(config.Profile) (string, error)) *onePasswordProvider {
	p := &onePasswordProvider{version: version, desktop: desktop, service: service, token: token, now: time.Now}
	p.failures.key = make([]byte, 32)
	_, _ = rand.Read(p.failures.key)
	return p
}

func NewOnePasswordProvider(version string, o OnePasswordOptions) Provider {
	var desktop clientFactory
	if o.DesktopApp {
		desktop = func(ctx context.Context, account, version string) (SecretClient, error) {
			return newSDKSecrets(ctx, func() (*onepassword.Client, error) {
				return onepassword.NewClient(ctx, onepassword.WithDesktopAppIntegration(account), onepassword.WithIntegrationInfo("MCParcel", version))
			})
		}
	}
	env := o.Env
	p := newOnePasswordProvider(version, desktop, func(ctx context.Context, token, version string) (SecretClient, error) {
		return newSDKSecrets(ctx, func() (*onepassword.Client, error) {
			return onepassword.NewClient(ctx, onepassword.WithServiceAccountToken(token), onepassword.WithIntegrationInfo("MCParcel", version))
		})
	}, func(profile config.Profile) (string, error) { return ServiceAccountToken(profile, env) })
	if o.Now != nil {
		p.now = o.Now
	}
	return p
}

func newSDKSecrets(ctx context.Context, create func() (*onepassword.Client, error)) (SecretClient, error) {
	c, err := constructSDKClient(create)
	if err != nil {
		return nil, safeProviderError(ctx, classifySDKError(err))
	}
	return sdkSecrets(c), nil
}

// classifySDKError maps a rate limit to ErrRateLimited and returns any other
// error unchanged for safeProviderError to reduce. DesktopSessionExpiredError
// is left alone: the SDK re-authorizes on it itself and returns the retry's
// error raw.
func classifySDKError(err error) error {
	var limited *onepassword.RateLimitExceededError
	if errors.As(err, &limited) {
		return ErrRateLimited
	}
	return err
}

func (p *onePasswordProvider) Bootstrap(ctx context.Context, profile config.Profile) (SecretClient, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// A service-account token selects its own account, so it is served
	// before the desktop account pin and without the desktop app.
	if profile.PromptFree() {
		return p.bootstrapToken(ctx, profile)
	}
	if p.desktop == nil {
		return nil, ErrProvider
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
	if profile.Mode == config.ProfileModeDesktop {
		// The desktop client itself serves the session; BootstrapRef is unused.
		return safeSecretClient{client: desktop}, nil
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

// bootstrapToken builds a service-account client from the profile's token. A
// token 1Password refused recently is answered from the negative cache
// without a network call; a rate limit is never cached.
func (p *onePasswordProvider) bootstrapToken(ctx context.Context, profile config.Profile) (SecretClient, error) {
	token, err := p.token(profile)
	if err != nil {
		return nil, safeProviderError(ctx, err)
	}
	fp := p.failures.fingerprint(token)
	if p.failures.blocked(fp, p.now()) {
		token = ""
		return nil, ErrProvider
	}
	service, err := p.service(ctx, token, p.version)
	token = ""
	if err != nil {
		err = safeProviderError(ctx, err)
		if !errors.Is(err, ErrRateLimited) && ctx.Err() == nil {
			p.failures.record(fp, p.now())
		}
		return nil, err
	}
	p.failures.forget(fp)
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
	if err != nil {
		return "", classifySDKError(err)
	}
	return value, nil
}

var sdkConstructionMu sync.Mutex

func constructSDKClient(create func() (*onepassword.Client, error)) (*onepassword.Client, error) {
	sdkConstructionMu.Lock()
	defer sdkConstructionMu.Unlock()
	return create()
}
