package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dedene/mcparcel/internal/spike"
)

// SpikeCmd groups the stage-1 feasibility probes. It is hidden and is deleted
// when stage 2 starts.
type SpikeCmd struct {
	Wait      SpikeWaitCmd      `cmd:"" help:"Print 'ready' and block until interrupted."`
	Op        SpikeOpCmd        `cmd:"" help:"Prove desktop bootstrap -> service account -> one secret."`
	Keychain  SpikeKeychainCmd  `cmd:"" help:"Store, read or remove a throwaway Keychain item."`
	HTTPProbe SpikeHTTPProbeCmd `cmd:"" name:"http-probe" help:"Probe transport and OAuth discovery of HTTP servers without credentials."`
}

// SpikeWaitCmd lets the packaging tests check signal propagation.
type SpikeWaitCmd struct{}

// Run blocks until the context is cancelled.
func (c *SpikeWaitCmd) Run(ctx context.Context, s *Streams) error {
	fmt.Fprintln(s.Out, "ready")
	<-ctx.Done()
	return ctx.Err()
}

// SpikeOpCmd runs the 1Password bootstrap proof.
type SpikeOpCmd struct {
	Account      string        `required:"" help:"Account name as shown in the 1Password desktop app."`
	BootstrapRef string        `required:"" help:"op:// reference of the test service-account token."`
	SecretRef    string        `required:"" help:"op:// reference of a test secret the service account can read."`
	Repeat       int           `default:"3" help:"Number of service-account resolves."`
	Timeout      time.Duration `default:"120s" help:"Maximum time for the 1Password probe."`
}

// Run implements the op probe.
func (c *SpikeOpCmd) Run(ctx context.Context, s *Streams) error {
	return spike.OpBootstrap(ctx, s.Out, spike.OpBootstrapOptions{
		Account: c.Account, BootstrapRef: c.BootstrapRef, SecretRef: c.SecretRef,
		Repeat: c.Repeat, Version: version, Timeout: c.Timeout,
	})
}

// SpikeKeychainCmd groups the Keychain probe actions.
type SpikeKeychainCmd struct {
	Set    SpikeKeychainSetCmd    `cmd:"" help:"Store a random throwaway value."`
	Get    SpikeKeychainGetCmd    `cmd:"" help:"Read the throwaway value back."`
	Remove SpikeKeychainRemoveCmd `cmd:"" help:"Delete the throwaway value."`
}

type (
	// SpikeKeychainSetCmd stores the probe item.
	SpikeKeychainSetCmd struct{}
	// SpikeKeychainGetCmd reads the probe item.
	SpikeKeychainGetCmd struct{}
	// SpikeKeychainRemoveCmd deletes the probe item.
	SpikeKeychainRemoveCmd struct{}
)

// Run implements keychain set.
func (c *SpikeKeychainSetCmd) Run(s *Streams) error { return spike.KeychainSet(s.Out) }

// Run implements keychain get.
func (c *SpikeKeychainGetCmd) Run(s *Streams) error { return spike.KeychainGet(s.Out) }

// Run implements keychain remove.
func (c *SpikeKeychainRemoveCmd) Run(s *Streams) error { return spike.KeychainRemove(s.Out) }

// SpikeHTTPProbeCmd probes the HTTP servers of an mcporter config.
type SpikeHTTPProbeCmd struct {
	Config string   `required:"" type:"existingfile" help:"Path to mcporter.json. Read only."`
	Only   []string `help:"Limit the probe to these server names."`
}

// Run implements the HTTP probe and prints a JSON array without URLs.
func (c *SpikeHTTPProbeCmd) Run(ctx context.Context, s *Streams) error {
	reports, err := spike.ProbeConfig(ctx, c.Config, c.Only)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(s.Out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(reports)
}
