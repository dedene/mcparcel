// Package spike holds stage-1 feasibility probes. Nothing here is a product
// API; the package is deleted when stage 2 starts.
package spike

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/1password/onepassword-sdk-go"
)

// OpBootstrapOptions configures the desktop -> service-account proof.
type OpBootstrapOptions struct {
	Account      string // account name as shown in the 1Password desktop app
	BootstrapRef string // op:// reference of the service-account token
	SecretRef    string // op:// reference readable by that service account
	Repeat       int    // how often to resolve SecretRef through the service account
	Version      string
	Timeout      time.Duration
}

// OpBootstrap reads a service-account token through the desktop app, then
// resolves one secret through the service account. It writes timings and
// value lengths only, never a value.
func OpBootstrap(ctx context.Context, out io.Writer, o OpBootstrapOptions) error {
	return runWithTimeout(ctx, o.Timeout, func(ctx context.Context) error {
		return opBootstrap(ctx, out, o)
	})
}

func runWithTimeout(ctx context.Context, d time.Duration, fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	if ctx.Err() != nil {
		return fmt.Errorf("1Password did not answer within %s", d)
	}
	result := make(chan error, 1)
	go func() { result <- fn(ctx) }()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return fmt.Errorf("1Password did not answer within %s", d)
	}
}

func opBootstrap(ctx context.Context, out io.Writer, o OpBootstrapOptions) error {
	start := time.Now()
	desktop, err := onepassword.NewClient(ctx,
		onepassword.WithDesktopAppIntegration(o.Account),
		onepassword.WithIntegrationInfo("MCParcel", o.Version),
	)
	if err != nil {
		return fmt.Errorf("desktop client: %w", err)
	}
	token, err := desktop.Secrets().Resolve(ctx, o.BootstrapRef)
	if err != nil {
		return fmt.Errorf("resolve bootstrap reference through the desktop app: %w", err)
	}
	fmt.Fprintf(out, "desktop bootstrap: ok, token length %d, %s\n", len(token), time.Since(start).Round(time.Millisecond))

	start = time.Now()
	account, err := onepassword.NewClient(ctx,
		onepassword.WithServiceAccountToken(token),
		onepassword.WithIntegrationInfo("MCParcel", o.Version),
	)
	if err != nil {
		return fmt.Errorf("service-account client: %s", redact(err, token))
	}
	fmt.Fprintf(out, "service-account client: ok, %s\n", time.Since(start).Round(time.Millisecond))

	for i := 1; i <= o.Repeat; i++ {
		start = time.Now()
		value, err := account.Secrets().Resolve(ctx, o.SecretRef)
		if err != nil {
			return fmt.Errorf("resolve %d through the service account: %s", i, redact(err, token))
		}
		fmt.Fprintf(out, "service-account resolve %d: ok, value length %d, %s\n", i, len(value), time.Since(start).Round(time.Millisecond))
	}
	return nil
}

// redact returns the error text with the bootstrap token removed. Errors from
// the desktop stage are produced before a token exists and are passed through,
// because the live proof records their exact wording.
func redact(err error, token string) string {
	if token == "" {
		return err.Error()
	}
	return strings.ReplaceAll(err.Error(), token, "[redacted]")
}
