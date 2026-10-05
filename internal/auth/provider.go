package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dedene/mcparcel/internal/config"
)

var (
	ErrAccountConflict = fmt.Errorf("desktop account conflict: %w", ErrProvider)
	ErrRequired        = errors.New("credential authorization required")
	ErrExpired         = errors.New("credential session expired")
	ErrProvider        = errors.New("credential provider failed")
)

type SecretClient interface {
	Resolve(context.Context, string) (string, error)
}
type Provider interface {
	Bootstrap(context.Context, config.Profile) (SecretClient, error)
}
type Lease struct {
	Identity         string
	ExpiresAt        time.Time
	SessionExpiresAt time.Time
	Values           map[string]string
}
type Resolver interface {
	Resolve(context.Context, string, config.Profile, []string, bool) (Lease, error)
	Close() error
}
type ResolverOptions struct {
	Provider      Provider
	Now           func() time.Time
	AuthTimeout   time.Duration
	LeaseDuration time.Duration
}

func safeProviderError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, ErrAccountConflict) {
		return ErrAccountConflict
	}
	if errors.Is(err, ErrExpired) {
		return ErrExpired
	}
	if errors.Is(err, ErrRequired) {
		return ErrRequired
	}
	return ErrProvider
}
