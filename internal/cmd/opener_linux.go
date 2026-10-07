//go:build !mcparceltest

package cmd

import (
	"context"
	"errors"

	"github.com/dedene/mcparcel/internal/config"
)

// errNotAvailable answers a browser or dialog request on Linux, which has
// neither: auth login then shows only the URL, and an approval dialog
// counts as declined.
var errNotAvailable = errors.New("not available on Linux")

// browserOpens reports whether newBrowser can open a browser at all; auth
// login words its prompt by it.
const browserOpens = false

func newBrowser(config.Paths) func(context.Context, string) error {
	return func(context.Context, string) error { return errNotAvailable }
}

func newDialog(config.Paths) func(context.Context, []string) (string, error) {
	return func(context.Context, []string) (string, error) { return "", errNotAvailable }
}
