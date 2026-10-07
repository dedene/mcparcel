// Package cmd wires the mcparcel command line.
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/alecthomas/kong"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/catalog"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

// Exit codes follow docs/cli.md.
const (
	ExitOK          = 0
	ExitInternal    = 1
	ExitUsage       = 2
	ExitInterrupted = 130
)

// Streams carries the process streams so commands never touch os.Stdout directly.
type Streams struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// CLI is the root command tree.
type CLI struct {
	Add     AddCmd     `cmd:"" json:"-" help:"Register a repository catalog."`
	Remove  RemoveCmd  `cmd:"" json:"-" help:"Remove a repository catalog offline."`
	Sync    SyncCmd    `cmd:"" json:"-" help:"Preview or apply repository catalog updates."`
	Local   LocalCmd   `cmd:"" json:"-" help:"Add, update or remove personal definitions offline."`
	Enable  EnableCmd  `cmd:"" json:"-" help:"Enable and accept selected connections offline."`
	Disable DisableCmd `cmd:"" json:"-" help:"Disable selected connections offline."`
	Catalog CatalogCmd `cmd:"" json:"-" help:"Show all connection metadata offline."`
	List    ListCmd    `cmd:"" json:"-" help:"Show enabled connections offline."`
	Inspect InspectCmd `cmd:"" json:"-" help:"Inspect a connection offline."`
	Config  ConfigCmd  `cmd:"" json:"-" help:"Validate and edit local configuration offline."`
	Import  ImportCmd  `cmd:"" json:"-" help:"Preview and apply imported definitions offline."`
	JSON    bool       `help:"Print one JSON envelope."`
	NoInput bool       `help:"Do not prompt or initiate credential authorization."`
	Version VersionCmd `cmd:"" help:"Print the mcparcel version."`
	Tools   ToolsCmd   `cmd:"" help:"List a connection's tools."`
	Call    CallCmd    `cmd:"" help:"Call an MCP tool."`
	Auth    AuthCmd    `cmd:"" help:"Sign in to, inspect or sign out of OAuth connections."`
	Runtime RuntimeCmd `cmd:"" help:"Inspect, restart or stop the runtime."`
	Daemon  DaemonCmd  `cmd:"" hidden:""`

	// VersionFlag is --version; it prints what the version command prints.
	VersionFlag versionFlag `name:"version" help:"Print the mcparcel version and exit."`

	// PlatformCommands adds the hidden macOS-only spike command.
	PlatformCommands `embed:""`
}

type CommandOptions struct {
	JSON    bool
	NoInput bool
}

type commandFailure struct {
	Data    any
	Failure *output.Error
}

func (e *commandFailure) Error() string { return e.Failure.Error() }
func (e *commandFailure) Unwrap() error { return e.Failure }

type commandWriteFailure struct{ error }

type exitSignal int

// Run parses args, executes the selected command and returns the process exit code.
func Run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) (code int) {
	defer func() {
		if r := recover(); r != nil {
			sig, ok := r.(exitSignal)
			if !ok {
				panic(r)
			}
			code = int(sig)
		}
	}()

	var cli CLI
	opts := &CommandOptions{}
	intent := scanIntent(args)
	parser, err := kong.New(&cli,
		kong.Name("mcparcel"),
		kong.Description("One CLI for every MCP call: team catalogs, personal selections, on-demand credentials."),
		kong.Writers(out, errOut),
		kong.Exit(func(c int) { panic(exitSignal(c)) }),
		kong.BindTo(ctx, (*context.Context)(nil)),
		kong.Bind(&Streams{In: in, Out: out, Err: errOut}),
		kong.Bind(opts),
		kong.BindToProvider(catalogDependencies),
	)
	if err != nil {
		fmt.Fprintf(errOut, "mcparcel: %v\n", err)
		return ExitInternal
	}

	kctx, err := parser.Parse(args)
	if err != nil {
		if intent.product {
			return writeFailure(out, errOut, intent.json, usageFailure(intent.command))
		}
		fmt.Fprintf(errOut, "mcparcel: %v\nRun 'mcparcel --help' for usage.\n", err)
		return ExitUsage
	}
	opts.JSON, opts.NoInput = cli.JSON, cli.NoInput
	if intent.command == "config" || intent.command == "import" || intent.command == "local" {
		if intent.flags["--file"] > 1 || intent.flags["--bindings"] > 1 || intent.flags["empty"] > 0 {
			return writeFailure(out, errOut, intent.json, usageFailure(intent.command))
		}
	}
	if intent.flags["--domain"] > 1 || intent.command == "catalog" && intent.flags["empty"] > 0 {
		return writeFailure(out, errOut, intent.json, usageFailure(intent.command))
	}
	if intent.command == "add" || intent.command == "sync" {
		if intent.flags["--path"] > 1 || intent.flags["--ref"] > 1 || intent.flags["empty"] > 0 {
			return writeFailure(out, errOut, intent.json, usageFailure(intent.command))
		}
	}
	if intent.command == "call" {
		if err := validateCallSources(&cli.Call, intent.flags); err != nil {
			return writeFailure(out, errOut, opts.JSON, err)
		}
	}
	if err := kctx.Run(); err != nil {
		if intent.product {
			var writeErr *commandWriteFailure
			if errors.As(err, &writeErr) {
				return ExitInternal
			}
			return writeFailure(out, errOut, opts.JSON, contextualFailure(intent.command, err))
		}
		if errors.Is(err, context.Canceled) {
			return ExitInterrupted
		}
		fmt.Fprintf(errOut, "mcparcel: %v\n", err)
		return ExitInternal
	}
	return ExitOK
}

type parseIntent struct {
	json    bool
	product bool
	command string
	flags   map[string]int
}

func usageFailure(command string) *output.Error {
	if command == "call" {
		return output.NewError("invalid_arguments", nil)
	}
	return output.CommandUsageError()
}

func contextualFailure(command string, err error) error {
	if command != "call" && errors.Is(err, args.ErrInvalidArgs) {
		return output.CommandUsageError()
	}
	return err
}

// Payload values are opaque, including values that look like flags.
func scanIntent(argv []string) parseIntent {
	i := parseIntent{flags: map[string]int{}}
	for n := 0; n < len(argv); n++ {
		token := argv[n]
		if token == "--" {
			break
		}
		name, value, inline := strings.Cut(token, "=")
		switch name {
		case "--args", "--args-file", "--timeout", "--meta", "--output-dir", "--file", "--bindings", "--only", "--domain", "--path", "--ref", "--accept":
			i.flags[name]++
			if name == "--file" || name == "--bindings" || name == "--only" || name == "--domain" || name == "--path" || name == "--ref" || name == "--accept" {
				if inline && value == "" || !inline && (n+1 == len(argv) || argv[n+1] == "") {
					i.flags["empty"]++
				}
			}
			if !inline {
				n++
			}
			continue
		}
		switch token {
		case "--json", "--json=true":
			i.json = true
		case "--json=false":
			i.json = false
		}
		if i.command == "" && !strings.HasPrefix(token, "-") {
			i.command = token
			i.product = token == "tools" || token == "call" || token == "auth" || token == "runtime" || token == "daemon" || token == "config" || token == "import" || token == "catalog" || token == "list" || token == "inspect" || token == "enable" || token == "disable" || token == "local" || token == "add" || token == "remove" || token == "sync"
		}
	}
	return i
}

func safeFailure(err error) *output.Error {
	var blocked *config.ImportBlockedError
	if errors.As(err, &blocked) {
		raw, e := json.Marshal(blocked.Report)
		if e != nil {
			return output.NewError("internal_error", nil)
		}
		return output.NewError("import_blocked", &output.Details{ImportReport: raw})
	}
	var ambiguous *config.AmbiguousIDError
	if errors.As(err, &ambiguous) {
		return output.NewError("ambiguous_id", &output.Details{Candidates: ambiguous.Candidates})
	}
	var safe *output.Error
	if errors.As(err, &safe) && safe != nil {
		return safe
	}
	if errors.Is(err, config.ErrHeadlessOnly) {
		return output.HeadlessOnlyError()
	}
	code := "internal_error"
	switch {
	case errors.Is(err, catalog.ErrOffline):
		code = "catalog_offline"
	case errors.Is(err, catalog.ErrUnauthorized):
		code = "catalog_auth_required"
	case errors.Is(err, catalog.ErrGHRequired):
		code = "catalog_gh_required"
	case errors.Is(err, catalog.ErrRateLimited):
		code = "catalog_rate_limited"
	case errors.Is(err, catalog.ErrRepositoryMissing):
		code = "catalog_unavailable"
	case errors.Is(err, catalog.ErrSourceRenamed):
		code = "catalog_renamed"
	case errors.Is(err, catalog.ErrRepositoryReused):
		code = "catalog_identity_changed"
	case errors.Is(err, catalog.ErrSourceConflict):
		code = "catalog_source_conflict"
	case errors.Is(err, catalog.ErrContentInvalid):
		code = "invalid_catalog"
	case errors.Is(err, catalog.ErrContentTooLarge):
		code = "catalog_too_large"
	case errors.Is(err, catalog.ErrLocalBindings):
		code = "catalog_bindings_conflict"
	case errors.Is(err, args.ErrInvalidArgs):
		code = "invalid_arguments"
	case errors.Is(err, config.ErrUnsafePath):
		code = "unsafe_local_path"
	case errors.Is(err, config.ErrConfigConflict):
		code = "config_conflict"
	case errors.Is(err, config.ErrConfigReadOnly):
		code = "config_read_only"
	case errors.Is(err, config.ErrConfig), errors.Is(err, config.ErrRevisionExhausted):
		code = "invalid_config"
	case errors.Is(err, config.ErrAliasCollision):
		code = "alias_collision"
	case errors.Is(err, config.ErrNotFound):
		code = "connection_unavailable"
	case errors.Is(err, config.ErrDisabled):
		code = "connection_disabled"
	case errors.Is(err, config.ErrReviewRequired):
		code = "review_required"
	case errors.Is(err, config.ErrToolDenied):
		code = "tool_denied"
	case errors.Is(err, config.ErrRuntimeUnsupported):
		code = "runtime_unsupported"
	case errors.Is(err, config.ErrConfigRequired):
		code = "config_required"
	case errors.Is(err, context.DeadlineExceeded):
		code = "timeout"
	case errors.Is(err, context.Canceled):
		code = "canceled"
	case errors.Is(err, config.ErrConfigWrite), errors.Is(err, config.ErrDurability):
		code = "config_write_failed"
	}
	return output.NewError(code, nil)
}

func writeFailure(out, errOut io.Writer, jsonMode bool, err error) int {
	failure := safeFailure(err)
	var data any
	var result *commandFailure
	if errors.As(err, &result) {
		data = result.Data
	}
	if jsonMode {
		if output.WriteJSON(out, data, failure) != nil {
			return ExitInternal
		}
	} else {
		var blocked *config.ImportBlockedError
		if errors.As(err, &blocked) {
			data = renderImport(blocked.Report)
		}
		if data != nil && output.WriteHuman(out, data) != nil {
			return ExitInternal
		}
		if output.WriteHuman(errOut, failure) != nil {
			return ExitInternal
		}
	}
	return output.ExitCode(failure)
}

func writeSuccess(s *Streams, opts *CommandOptions, data any) error {
	var err error
	if opts.JSON {
		err = output.WriteJSON(s.Out, data, nil)
	} else {
		err = output.WriteHuman(s.Out, data)
	}
	if err != nil {
		return &commandWriteFailure{err}
	}
	return nil
}
