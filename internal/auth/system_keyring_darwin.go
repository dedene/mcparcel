package auth

import "github.com/zalando/go-keyring"

// SystemKeyring is the macOS Keychain through go-keyring.
type SystemKeyring struct{}

func (SystemKeyring) Get(service, account string) (string, error) {
	v, err := keyring.Get(service, account)
	return v, notFound(err)
}

func (SystemKeyring) Set(service, account, secret string) error {
	return keyring.Set(service, account, secret)
}

func (SystemKeyring) Delete(service, account string) error {
	return notFound(keyring.Delete(service, account))
}

// KeyringReachable reports whether the system keyring can be reached from
// this process. The macOS Keychain always can.
func KeyringReachable() bool { return true }

// BusAddressFrom reports whether value is a D-Bus session bus address the
// daemon may inherit. macOS has no session bus, so none is.
func BusAddressFrom(string) bool { return false }
