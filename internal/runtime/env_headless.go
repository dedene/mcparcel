package runtime

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// headlessBaseNames are the variables a headless runtime always keeps from
// its own environment (D11). No login shell runs in headless mode.
var headlessBaseNames = []string{"HOME", "LANG", "LC_ALL", "PATH", "TMPDIR", "USER"}

// HeadlessEnv returns the daemon's environment restricted to names: base names plus every env: ref and inheritEnv name of enabled connections.
// The base names are always kept; an entry without "=" or with an empty
// name is dropped, the first of duplicate entries wins (as os.Getenv), and
// an empty PATH falls back to the default.
func HeadlessEnv(environ []string, names []string) map[string]string {
	keep := make(map[string]bool, len(headlessBaseNames)+len(names))
	for _, name := range headlessBaseNames {
		keep[name] = true
	}
	for _, name := range names {
		keep[name] = true
	}
	out := map[string]string{}
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" || !keep[key] {
			continue
		}
		if _, seen := out[key]; !seen {
			out[key] = value
		}
	}
	if out["PATH"] == "" {
		out["PATH"] = defaultPath
	}
	return out
}

// ForwardedNames returns the base names plus the variables enabled
// connections reference (env: refs and stdio inheritEnv), sorted and
// deduplicated. Names the runtime reads itself (XDG_*, MCPARCEL_*) and
// names that change how a process loads or starts (forbiddenChildEnv) are
// never forwarded; a reference to one is then reported as not set.
func ForwardedNames(snap config.Snapshot) []string {
	set := map[string]bool{}
	for _, name := range headlessBaseNames {
		set[name] = true
	}
	if snap.Effective != nil {
		for _, row := range snap.Effective.Connections {
			if !row.Enabled || row.Connection == nil {
				continue
			}
			names := config.EnvRefs(*row.Connection)
			if stdio := row.Connection.Transport.Stdio; stdio != nil {
				names = append(names, stdio.InheritEnv...)
			}
			for _, name := range names {
				if forwardable(name) {
					set[name] = true
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

func forwardable(name string) bool {
	if name == "" || strings.ContainsAny(name, "=\x00") || forbiddenChildEnv(name) {
		return false
	}
	return !strings.HasPrefix(name, "XDG_") && !strings.HasPrefix(name, "MCPARCEL_")
}

// forwardedNamesFor reads the configuration the daemon will serve; when it
// cannot be read, only the base names are forwarded and the daemon reports
// the configuration error itself.
func forwardedNamesFor(p config.Paths) []string {
	snap, err := config.Load(p)
	if err != nil {
		return ForwardedNames(config.Snapshot{})
	}
	return ForwardedNames(snap)
}

// headlessDaemonEnvironment is the environment the CLI gives a headless
// daemon it starts: the forwarded names from environ, then HOME and
// XDG_CONFIG_HOME, which locate the configuration and always win.
func headlessDaemonEnvironment(p config.Paths, environ []string, names []string) []string {
	env := HeadlessEnv(environ, names)
	env["HOME"], env["XDG_CONFIG_HOME"] = p.Home, filepath.Dir(p.ConfigDir)
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// HeadlessDaemonEnv is the headless runtime's environment source: its own
// environment restricted to the forwarded names of the configuration it
// serves. It never runs a login shell.
func HeadlessDaemonEnv(p config.Paths) map[string]string {
	return HeadlessEnv(os.Environ(), forwardedNamesFor(p))
}

// headlessMissingEnv is config_required for env: references a headless
// runtime does not have; it names the variables, never a value.
func headlessMissingEnv(names []string) *output.Error {
	err := output.NewError("config_required", &output.Details{Variables: names})
	list := strings.Join(names, ", ")
	if len(names) == 1 {
		err.Message = "Environment variable " + list + " is not set."
	} else {
		err.Message = "Environment variables " + list + " are not set."
	}
	err.NextAction = "Set " + list + " in the environment that starts mcparcel (headless mode reads no Keychain), then run mcparcel runtime restart."
	return err
}

// headlessSignIn is auth_required for a server that asks a headless runtime
// for credentials: it names the connection's env: variables, if any.
func headlessSignIn(names []string) *output.Error {
	err := output.NewError("auth_required", nil)
	if len(names) == 0 {
		err.Message = "This server needs sign-in, which headless mode cannot do."
		err.NextAction = "Give the connection a credential header from an env: reference, or OAuth client credentials."
		return err
	}
	list := strings.Join(names, ", ")
	if len(names) == 1 {
		err.Message = "The server rejected the credentials from environment variable " + list + "."
	} else {
		err.Message = "The server rejected the credentials from environment variables " + list + "."
	}
	err.NextAction = "Check " + list + " in the environment that starts mcparcel, update the value, then run mcparcel runtime restart."
	return err
}

// headlessResolver stands in for the 1Password resolver in headless mode: it
// refuses every reference without contacting 1Password.
type headlessResolver struct{}

func (headlessResolver) Resolve(context.Context, string, config.Profile, []string, bool) (auth.Lease, error) {
	return auth.Lease{}, output.HeadlessOnePasswordError()
}

func (headlessResolver) Close() error { return nil }
