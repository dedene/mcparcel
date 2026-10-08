package output

import (
	"fmt"
	"testing"
)

func TestKeyringErrors(t *testing.T) {
	for _, tc := range []struct {
		name          string
		err           *Error
		message, next string
	}{
		{
			"unreachable", KeyringUnreachableError(),
			"No Secret Service keyring is reachable from this session (no usable D-Bus session bus), so MCParcel cannot store a sign-in.",
			"Run mcparcel auth <mcp> from your desktop session. Without one, use headless mode with OAuth client credentials, env: references or a service-account profile.",
		},
		{
			"prompt pending", KeyringPromptPendingError(),
			"A keyring prompt from an earlier MCParcel request is still open.",
			"Answer or dismiss the keyring dialog in your desktop session, then run the command again. If no dialog is visible, run mcparcel runtime restart.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.err
			if e.Code != "keychain_unavailable" || e.Message != tc.message || e.NextAction != tc.next || e.Details != nil ||
				ExitCode(e) != 3 || ExitCode(fmt.Errorf("wrapped: %w", e)) != 3 {
				t.Fatal(e, ExitCode(e))
			}
			if NewError("keychain_unavailable", nil).Message == e.Message {
				t.Fatal("constructor changed the registry entry")
			}
		})
	}
}

func TestDesktopAppUnavailableError(t *testing.T) {
	e := DesktopAppUnavailableError("work\x1b[31m")
	if e.Code != "config_required" || ExitCode(e) != 2 ||
		e.Message != `Profile "work\x1b[31m" uses the 1Password desktop app, which MCParcel supports on macOS only.` ||
		e.NextAction != `Bind this connection to a service-account profile (mode "service-account" with tokenEnv or tokenFile) with mcparcel config profile bind.` {
		t.Fatal(e.Message, e.NextAction)
	}
	if e := DesktopAppUnavailableError("work"); e.Message != "Profile work uses the 1Password desktop app, which MCParcel supports on macOS only." {
		t.Fatal(e.Message)
	}
}

func TestLinuxNoSessionError(t *testing.T) {
	e := LinuxNoSessionError()
	if e.Code != "runtime_unsupported" || ExitCode(e) != 2 || e.Details != nil ||
		e.Message != "No MCParcel configuration was found and there is no desktop session." ||
		e.NextAction != `In a container or service, point XDG_CONFIG_HOME at the directory that holds mcparcel/config.json and set runtime.mode to "headless". On a workstation, run mcparcel import, mcparcel add or mcparcel setup first.` {
		t.Fatal(e.Message, e.NextAction)
	}
	if NewError("runtime_unsupported", nil).Message == e.Message || HeadlessOnlyError().Message == e.Message {
		t.Fatal("LinuxNoSessionError changed a shared text")
	}
}
