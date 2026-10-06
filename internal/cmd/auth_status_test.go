package cmd

import (
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

// "off" wins over "unavailable": a connection whose client secret is a
// 1Password reference can still have keep-alive switched off.
func TestKeepAliveSettingOffWins(t *testing.T) {
	secret := &config.Value{Secret: &config.SecretRef{Secret: "op://vault/a/secret"}}
	for _, tc := range []struct {
		lifecycle *config.Lifecycle
		secret    *config.Value
		want      string
	}{
		{nil, nil, "24h"},
		{&config.Lifecycle{KeepAlive: "12h"}, nil, "12h"},
		{&config.Lifecycle{KeepAlive: "off"}, nil, "off"},
		{nil, secret, "unavailable"},
		{&config.Lifecycle{KeepAlive: "off"}, secret, "off"},
	} {
		c := config.Connection{Lifecycle: tc.lifecycle, Auth: &config.OAuth{Type: "oauth", ClientSecret: tc.secret}}
		if got := keepAliveSetting(c); got != tc.want {
			t.Errorf("%+v %v: %q, want %q", tc.lifecycle, tc.secret != nil, got, tc.want)
		}
	}
}
