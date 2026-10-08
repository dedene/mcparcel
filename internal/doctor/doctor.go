// Package doctor computes the rows of mcparcel doctor from injected inputs.
// The offline checks never start a runtime, run a shell, read a credential
// store or touch the network; cmd gathers what they look at.
package doctor

import (
	"io/fs"
	"slices"
	"strings"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

// Row statuses.
const (
	OK   = "ok"
	Warn = "warn"
	Fail = "fail"
	Skip = "skip"
)

// Input is everything the offline checks look at. cmd fills it; tests fake it.
type Input struct {
	Version          string // CLI version
	DesktopSupported bool
	// DesktopSession is a display or usable session bus (always on macOS);
	// Linux desktop mode without one and without config.json is refused.
	DesktopSession bool
	// DesktopOnePassword is desktop mode with the 1Password desktop app
	// (macOS); without it only service-account profiles read 1Password.
	DesktopOnePassword bool
	// KeyringReachable reports a usable D-Bus session bus; nil where the
	// keyring is no Secret Service (macOS) or in headless mode.
	KeyringReachable func() bool
	Paths            config.Paths
	Runtime          config.RuntimeDefaults
	RuntimeErr       error             // ReadRuntime failure: runtime.* and storage.* rows skip
	Files            config.FileReport // config.CheckFiles
	FilesErr         error             // lock timeout or unsafe config dir
	Snapshot         *config.Snapshot  // nil unless Files.State resolved
	Target           string            // canonical ID, "" for all
	Probe            runtimeclient.Probe
	ProbeErr         error
	Supervised       bool
	Retain           bool              // a desktop runtime starts from a retained copy (runtime.binary)
	LookupEnv        func(string) bool // presence only
	PATH             string            // this process's PATH
	Stat             func(string) (fs.FileInfo, error)
	AppDirs          []string // where 1Password.app may live
	// TokenFile checks a service-account token file without reading it
	// (config.CheckTokenFile).
	TokenFile func(string) error
}

// Offline returns the offline rows in contract order.
func Offline(in Input) []output.DoctorCheck {
	var checks []output.DoctorCheck
	checks = append(checks, runtimeChecks(in)...)
	checks = append(checks, storageChecks(in)...)
	checks = append(checks, configChecks(in)...)
	checks = append(checks, onePasswordAppCheck(in)...)
	checks = append(checks, keyringCheck(in)...)
	checks = append(checks, connectionChecks(in)...)
	checks = append(checks, upgradeChecks(in)...)
	return order(checks)
}

// Mode is DoctorData.Mode: desktop, headless or unknown (config.json unreadable).
func Mode(in Input) string {
	switch {
	case in.RuntimeErr != nil:
		return "unknown"
	case in.Runtime.Mode == config.ModeHeadless:
		return config.ModeHeadless
	}
	return config.ModeDesktop
}

func headless(in Input) bool { return Mode(in) == config.ModeHeadless }

// Summarize counts the rows by status.
func Summarize(checks []output.DoctorCheck) output.DoctorSummary {
	var s output.DoctorSummary
	for _, c := range checks {
		switch c.Status {
		case OK:
			s.OK++
		case Warn:
			s.Warn++
		case Fail:
			s.Fail++
		case Skip:
			s.Skip++
		}
	}
	return s
}

// The row catalogue in contract order: global rows, then per-connection rows
// (sorted by subject first), then live.tools.
var (
	globalOrder     = []string{"runtime.mode", "runtime.version", "runtime.binary", "storage.dir", "config.file", "config.state", "version.catalog", "config.summary", "prereq.onepassword", "prereq.keyring"}
	connectionOrder = []string{"config.connection", "credentials.profile", "credentials.token", "credentials.reference", "credentials.env", "credentials.oauth", "prereq.command"}
)

func order(checks []output.DoctorCheck) []output.DoctorCheck {
	key := func(c output.DoctorCheck) (int, string, int) {
		if i := slices.Index(globalOrder, c.ID); i >= 0 {
			return 0, "", i
		}
		if i := slices.Index(connectionOrder, c.ID); i >= 0 {
			return 1, c.Subject, i
		}
		return 2, "", 0
	}
	slices.SortStableFunc(checks, func(a, b output.DoctorCheck) int {
		ga, sa, ra := key(a)
		gb, sb, rb := key(b)
		if ga != gb {
			return ga - gb
		}
		if c := strings.Compare(sa, sb); c != 0 {
			return c
		}
		return ra - rb
	})
	return checks
}

// nextAction is the registry's next action for code.
func nextAction(code string) string { return output.NewError(code, nil).NextAction }

// show cleans a value that goes into a message: local paths, versions, names.
func show(text string) string { return output.DisplayMetadata(text) }

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
