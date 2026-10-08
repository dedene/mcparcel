//go:build !darwin

package output

// The keychain_unavailable catalogue text: OAuth sign-ins live in a Secret
// Service keyring.
const (
	keychainUnavailableMessage = "No usable Secret Service keyring could store or read the sign-in."
	keychainUnavailableAction  = "Start and unlock GNOME Keyring, KWallet or another Secret Service provider in your desktop session, then run mcparcel runtime restart."
)
