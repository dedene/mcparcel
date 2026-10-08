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
	// ErrRateLimited keeps the session: the provider refused this request only.
	ErrRateLimited = errors.New("credential provider rate limited")
	// ErrLocked is the cause of work canceled by Lock.
	ErrLocked = errors.New("credentials locked")
	// ErrTokenUnavailable means a service-account profile's token is not set,
	// or its token file is missing, unreadable, empty or not a single token.
	ErrTokenUnavailable = errors.New("service-account token unavailable")
	// ErrTokenUnsafe means a service-account profile's token file failed the
	// ownership or permission check.
	ErrTokenUnsafe = errors.New("service-account token file unsafe")
)

type SecretClient interface {
	Resolve(context.Context, string) (string, error)
}
type Provider interface {
	Bootstrap(context.Context, config.Profile) (SecretClient, error)
}

// Resolver hands out leases over one shared provider session per profile.
type Resolver interface {
	Resolve(ctx context.Context, profileID string, profile config.Profile, refs []string, noInput bool) (Lease, error)
	// Invalidate drops the cached values of refs so the next Resolve reads
	// them again. It keeps the session and makes no provider call.
	Invalidate(profileID string, refs []string)
	// Lock ends every session and cancels in-flight work with cause ErrLocked.
	Lock()
	Sessions() []SessionInfo
	Close() error
}
type ResolverOptions struct {
	Provider      Provider
	Now           func() time.Time
	AuthTimeout   time.Duration
	LeaseDuration time.Duration
	// SweepEvery is how often expired values and sessions are purged without
	// a call. Default and maximum: one minute.
	SweepEvery time.Duration
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
	if errors.Is(err, ErrRateLimited) {
		return ErrRateLimited
	}
	if errors.Is(err, ErrTokenUnavailable) {
		return ErrTokenUnavailable
	}
	if errors.Is(err, ErrTokenUnsafe) {
		return ErrTokenUnsafe
	}
	return ErrProvider
}
