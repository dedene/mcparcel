package config

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestProfileModes(t *testing.T) {
	for _, c := range []struct {
		mode                   string
		promptFree, desktopApp bool
	}{
		{ProfileModeServiceAccount, true, false},
		{ProfileModeDesktopServiceAccount, false, true},
		{ProfileModeDesktop, false, true},
		{"", false, false},
	} {
		p := Profile{Mode: c.mode}
		if p.PromptFree() != c.promptFree || p.UsesDesktopApp() != c.desktopApp {
			t.Fatal(c.mode, p.PromptFree(), p.UsesDesktopApp())
		}
	}
}

// Existing profiles always carry an account, so account's omitempty and the
// new token fields leave their JSON, and every hash over it, unchanged.
func TestProfileMarshalUnchanged(t *testing.T) {
	for _, c := range []struct {
		p    Profile
		want string
	}{
		{Profile{Mode: ProfileModeDesktopServiceAccount, Account: "Fixture", BootstrapRef: "op://v/i/token", SessionDuration: "24h"}, `{"mode":"desktop-service-account","account":"Fixture","bootstrapRef":"op://v/i/token","sessionDuration":"24h"}`},
		{Profile{Mode: ProfileModeDesktop, Account: "Fixture", SessionDuration: "1h"}, `{"mode":"desktop","account":"Fixture","sessionDuration":"1h"}`},
		{Profile{Mode: ProfileModeServiceAccount, TokenEnv: "OP_SERVICE_ACCOUNT_TOKEN", SessionDuration: "24h"}, `{"mode":"service-account","tokenEnv":"OP_SERVICE_ACCOUNT_TOKEN","sessionDuration":"24h"}`},
	} {
		data, err := json.Marshal(c.p)
		if err != nil || string(data) != c.want {
			t.Fatal(string(data), err)
		}
	}
}

// Profile errors name the field path, never the value.
func TestProfileErrorsOmitValues(t *testing.T) {
	for _, profile := range []string{
		`{"mode":"secret-value"}`,
		`{"mode":"service-account","tokenEnv":"SECRET_VALUE"}`,
		`{"mode":"service-account","tokenEnv":"OP_SECRET-VALUE"}`,
		`{"mode":"service-account","tokenFile":"secret-value"}`,
		`{"mode":"service-account","tokenFile":"/secret-value/../x"}`,
		`{"mode":"service-account","tokenFile":"/secret-value\u001b"}`,
		`{"mode":"service-account","tokenEnv":"OP_SECRET_VALUE","tokenFile":"/secret-value"}`,
		`{"mode":"service-account","tokenEnv":"OP_SECRET_VALUE","account":"secret-value"}`,
		`{"mode":"service-account","tokenEnv":"OP_SECRET_VALUE","bootstrapRef":"op://secret-value/i/f"}`,
		`{"mode":"desktop","account":"Fixture","tokenEnv":"OP_SECRET_VALUE"}`,
		`{"mode":"desktop-service-account","account":"Fixture","bootstrapRef":"op://v/i/f","tokenFile":"/secret-value"}`,
	} {
		_, err := DecodeLocal([]byte(`{"schemaVersion":1,"credentialProfiles":{"agent":` + profile + `}}`))
		if !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), "credentialProfiles.agent") {
			t.Fatal(profile, err)
		}
		if text := strings.ToLower(err.Error()); strings.Contains(text, "secret-value") || strings.Contains(text, "secret_value") {
			t.Fatal("value in error:", err)
		}
	}
}

func TestProfileTokenNames(t *testing.T) {
	l := Local{CredentialProfiles: map[string]Profile{
		"b":    {Mode: ProfileModeServiceAccount, TokenEnv: "OP_B"},
		"a":    {Mode: ProfileModeServiceAccount, TokenEnv: "OP_A"},
		"a2":   {Mode: ProfileModeServiceAccount, TokenEnv: "OP_A"},
		"file": {Mode: ProfileModeServiceAccount, TokenFile: "/run/op-token"},
		"team": {Mode: ProfileModeDesktop, Account: "Fixture"},
	}}
	if got := ProfileTokenNames(l); !slices.Equal(got, []string{"OP_A", "OP_B"}) {
		t.Fatal(got)
	}
	if got := ProfileTokenNames(Local{}); len(got) != 0 {
		t.Fatal(got)
	}
}
