//go:build !mcparceltest

package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/dedene/mcparcel/internal/config"
)

// Linux has no browser opener and no approval dialog: both report
// errNotAvailable and run nothing.
func TestOpenerNotAvailable(t *testing.T) {
	ctx := context.Background()
	if err := newBrowser(config.Paths{})(ctx, "https://example.com/"); !errors.Is(err, errNotAvailable) {
		t.Fatal(err)
	}
	if out, err := newDialog(config.Paths{})(ctx, []string{"MCParcel", "Allow?"}); out != "" || !errors.Is(err, errNotAvailable) {
		t.Fatal(out, err)
	}
}
