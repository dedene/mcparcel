package auth

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

type localSecretClient struct {
	resolve func(context.Context, string) (string, error)
}

func (c localSecretClient) Resolve(ctx context.Context, ref string) (string, error) {
	return c.resolve(ctx, ref)
}

func TestRealProviderConstructionIsLazy(t *testing.T) {
	if NewOnePasswordProvider("test") == nil {
		t.Fatal("nil provider")
	}
}

func TestOnePasswordSequence(t *testing.T) {
	var steps []string
	desktop := func(_ context.Context, account, version string) (SecretClient, error) {
		if account != "fixture" || version != "test" {
			t.Fatal("desktop parameters")
		}
		steps = append(steps, "desktop")
		return localSecretClient{resolve: func(_ context.Context, ref string) (string, error) {
			if ref != "bootstrap" {
				t.Fatal("wrong bootstrap reference")
			}
			steps = append(steps, "bootstrap resolve")
			return "fake-token", nil
		}}, nil
	}
	service := func(_ context.Context, token, version string) (SecretClient, error) {
		if token != "fake-token" || version != "test" {
			t.Fatal("service parameters")
		}
		steps = append(steps, "service-account")
		return localSecretClient{resolve: func(_ context.Context, ref string) (string, error) {
			if ref != "requested" {
				t.Fatal("wrong requested reference")
			}
			steps = append(steps, "requested resolve")
			return "value", nil
		}}, nil
	}
	p := newOnePasswordProvider("test", desktop, service)
	c, e := p.Bootstrap(context.Background(), config.Profile{Account: "fixture", BootstrapRef: "bootstrap"})
	if e != nil {
		t.Fatal(e)
	}
	v, e := c.Resolve(context.Background(), "requested")
	if e != nil || v != "value" || !reflect.DeepEqual(steps, []string{"desktop", "bootstrap resolve", "service-account", "requested resolve"}) {
		t.Fatal("incorrect sequence")
	}
}

func TestOnePasswordBoundaryErrors(t *testing.T) {
	for _, phase := range []string{"desktop", "bootstrap", "service", "resolve"} {
		t.Run(phase, func(t *testing.T) {
			failure := errors.New("sensitive provider diagnostic")
			desktop := func(context.Context, string, string) (SecretClient, error) {
				if phase == "desktop" {
					return nil, failure
				}
				return localSecretClient{resolve: func(context.Context, string) (string, error) {
					if phase == "bootstrap" {
						return "", failure
					}
					return "token", nil
				}}, nil
			}
			service := func(context.Context, string, string) (SecretClient, error) {
				if phase == "service" {
					return nil, failure
				}
				return localSecretClient{resolve: func(context.Context, string) (string, error) { return "partial", failure }}, nil
			}
			c, e := newOnePasswordProvider("test", desktop, service).Bootstrap(context.Background(), config.Profile{})
			if phase == "resolve" {
				v, err := c.Resolve(context.Background(), "r")
				if v != "" {
					t.Fatal("partial value escaped")
				}
				e = err
			}
			if e != ErrProvider {
				t.Fatal("raw boundary error")
			}
		})
	}
}

func TestOnePasswordRejectsDifferentDesktopAccount(t *testing.T) {
	var accounts []string
	p := newOnePasswordProvider("test", func(_ context.Context, account, _ string) (SecretClient, error) {
		accounts = append(accounts, account)
		return localSecretClient{resolve: func(context.Context, string) (string, error) { return "token", nil }}, nil
	}, func(context.Context, string, string) (SecretClient, error) {
		return localSecretClient{resolve: func(context.Context, string) (string, error) { return "value", nil }}, nil
	})
	for range 2 {
		if _, err := p.Bootstrap(context.Background(), config.Profile{Account: "A"}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := p.Bootstrap(context.Background(), config.Profile{Account: "B"})
	if !errors.Is(err, ErrProvider) {
		t.Fatal("second account admitted", err)
	}
	if len(accounts) != 2 {
		t.Fatal("conflict reached SDK", accounts)
	}
}
