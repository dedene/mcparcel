package output

// KeyringUnreachableError is keychain_unavailable for a sign-in on Linux
// without a usable D-Bus session bus: nothing could store its tokens, so it
// is refused before a browser opens.
func KeyringUnreachableError() *Error {
	err := NewError("keychain_unavailable", nil)
	err.Message = "No Secret Service keyring is reachable from this session (no usable D-Bus session bus), so MCParcel cannot store a sign-in."
	err.NextAction = "Run mcparcel auth <mcp> from your desktop session. Without one, use headless mode with OAuth client credentials, env: references or a service-account profile."
	return err
}

// KeyringPromptPendingError is keychain_unavailable while an earlier keyring
// call still waits on a prompt (an unlock or create dialog) nobody answered.
func KeyringPromptPendingError() *Error {
	err := NewError("keychain_unavailable", nil)
	err.Message = "A keyring prompt from an earlier MCParcel request is still open."
	err.NextAction = "Answer or dismiss the keyring dialog in your desktop session, then run the command again. If no dialog is visible, run mcparcel runtime restart."
	return err
}

// DesktopAppUnavailableError is config_required for a profile that uses the
// 1Password desktop app in desktop mode where MCParcel cannot use the app
// (Linux): only service-account profiles read 1Password there.
func DesktopAppUnavailableError(id string) *Error {
	err := NewError("config_required", nil)
	err.Message = "Profile " + DisplayMetadata(id) + " uses the 1Password desktop app, which MCParcel supports on macOS only."
	err.NextAction = `Bind this connection to a service-account profile (mode "service-account" with tokenEnv or tokenFile) with mcparcel config profile bind.`
	return err
}

// LinuxNoSessionError is runtime_unsupported for desktop mode on Linux with
// neither a desktop session nor a config.json: most likely a container or
// service whose XDG_CONFIG_HOME misses its headless configuration.
func LinuxNoSessionError() *Error {
	err := NewError("runtime_unsupported", nil)
	err.Message = "No MCParcel configuration was found and there is no desktop session."
	err.NextAction = `In a container or service, point XDG_CONFIG_HOME at the directory that holds mcparcel/config.json and set runtime.mode to "headless". On a workstation, run mcparcel import, mcparcel add or mcparcel setup first.`
	return err
}
