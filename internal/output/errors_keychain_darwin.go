package output

// The keychain_unavailable catalogue text: OAuth sign-ins live in the macOS
// Keychain.
const (
	keychainUnavailableMessage = "The macOS Keychain could not store or read the sign-in."
	keychainUnavailableAction  = "Unlock the login keychain, then try again."
)
