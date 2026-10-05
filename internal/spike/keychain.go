package spike

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/keybase/go-keychain"
)

const (
	keychainService = "mcparcel-spike"
	keychainAccount = "probe"
)

// KeychainSet stores a random throwaway value in the login Keychain.
func KeychainSet(out io.Writer) error {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	value := []byte(hex.EncodeToString(raw))

	item := keychain.NewGenericPassword(keychainService, keychainAccount, "MCParcel feasibility probe", value, "")
	item.SetSynchronizable(keychain.SynchronizableNo)
	item.SetAccessible(keychain.AccessibleWhenUnlockedThisDeviceOnly)
	err := keychain.AddItem(item)
	if errors.Is(err, keychain.ErrorDuplicateItem) {
		return errors.New("probe item already exists: run 'mcparcel spike keychain remove' first")
	}
	if err != nil {
		return fmt.Errorf("add keychain item: %w", err)
	}
	fmt.Fprintf(out, "keychain set: ok, value length %d\n", len(value))
	return nil
}

// KeychainGet reads the probe value back. A macOS prompt here means the
// calling binary is not on the item's access list.
func KeychainGet(out io.Writer) error {
	value, err := keychain.GetGenericPassword(keychainService, keychainAccount, "", "")
	if err != nil {
		return fmt.Errorf("read keychain item: %w", err)
	}
	if value == nil {
		return errors.New("probe item not found: run 'mcparcel spike keychain set' first")
	}
	fmt.Fprintf(out, "keychain get: ok, value length %d\n", len(value))
	return nil
}

// KeychainRemove deletes the probe value.
func KeychainRemove(out io.Writer) error {
	if err := keychain.DeleteGenericPasswordItem(keychainService, keychainAccount); err != nil {
		return fmt.Errorf("delete keychain item: %w", err)
	}
	fmt.Fprintln(out, "keychain remove: ok")
	return nil
}
