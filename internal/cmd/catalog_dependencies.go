//go:build !mcparceltest

package cmd

import "github.com/dedene/mcparcel/internal/catalog"

func newCatalogFetcher() catalog.Fetcher { return catalog.NewGitHub() }
