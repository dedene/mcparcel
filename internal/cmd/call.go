package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/elicit"
	"github.com/dedene/mcparcel/internal/output"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

type CallCmd struct {
	Target      string   `arg:"" required:"" help:"Connection ID and tool name, separated by a dot."`
	Assignments []string `arg:"" optional:"" sep:"none" help:"Arguments as key=value, key:value or key:=json."`
	Args        string   `name:"args" help:"Arguments as one JSON object."`
	ArgsFile    string   `name:"args-file" help:"Read a JSON object from a file, or - for stdin."`
	Timeout     string   `name:"timeout" help:"Positive call duration, including queue and initialization; time spent answering a prompt is not counted."`
	Meta        string   `name:"meta" help:"JSON object sent as the call's _meta."`
	OutputDir   string   `name:"output-dir" help:"Save image and audio blocks as new files in this existing directory."`
}

var connectionID = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func splitCallTarget(target string) (connection, tool string, err error) {
	offset := 0
	if strings.HasPrefix(target, "github:") {
		hash := strings.IndexByte(target, '#')
		if hash < 0 {
			return "", "", output.CallTargetError()
		}
		offset = hash + 1
	}
	index := strings.IndexByte(target[offset:], '.')
	if index < 0 {
		return "", "", output.CallTargetError()
	}
	index += offset
	connection, tool = target[:index], target[index+1:]
	valid := connectionID.MatchString(connection)
	if strings.Contains(connection, ":") {
		valid = config.ValidateCanonicalID(connection) == nil
	}
	if !valid || tool == "" {
		return "", "", output.CallTargetError()
	}
	return connection, tool, nil
}

// callUsage is invalid_arguments for a call command line, pointing at call
// --help. reason names flags only, never their values.
func callUsage(reason, flag string) *output.Error {
	return output.UsageError("call", reason, flag, "")
}

func validateCallSources(c *CallCmd, flags map[string]int) error {
	sources := 0
	for _, flag := range []string{"--args", "--args-file", "--meta", "--output-dir"} {
		if flags[flag] > 1 {
			return callUsage("Give the "+flag+" flag only once.", flag)
		}
	}
	for _, flag := range []string{"--args", "--args-file"} {
		if flags[flag] > 0 {
			sources++
		}
	}
	if len(c.Assignments) > 0 {
		sources++
	}
	if sources > 1 {
		return callUsage("Give the arguments one way: key=value assignments, --args or --args-file.", "")
	}
	for _, f := range []struct{ flag, value string }{{"--args", c.Args}, {"--args-file", c.ArgsFile}, {"--meta", c.Meta}, {"--output-dir", c.OutputDir}} {
		if flags[f.flag] > 0 && f.value == "" {
			return callUsage("The "+f.flag+" flag needs a value.", f.flag)
		}
	}
	if flags["--timeout"] > 0 {
		if _, err := callTimeout(c.Timeout); err != nil {
			return err
		}
	}
	return nil
}

// callTimeout parses --timeout: a positive Go duration.
func callTimeout(text string) (time.Duration, error) {
	d, err := time.ParseDuration(text)
	if err != nil || d <= 0 {
		return 0, callUsage("The --timeout flag needs a positive duration, such as 120s.", "--timeout")
	}
	return d, nil
}

func (c *CallCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	connection, tool, err := splitCallTarget(c.Target)
	if err != nil {
		return err
	}
	var exports *output.ExportDir
	if c.OutputDir != "" {
		// Checked before any input is read or the runtime is contacted.
		if exports, err = output.OpenExportDir(c.OutputDir); err != nil {
			return err
		}
		defer exports.Close()
	}
	var timeout time.Duration
	if c.Timeout != "" {
		if timeout, err = callTimeout(c.Timeout); err != nil {
			return err
		}
	}
	var meta json.RawMessage
	if c.Meta != "" {
		// Validate the bytes the IPC frame carries: encoding compacts and HTML-escapes.
		if meta, err = json.Marshal(json.RawMessage(c.Meta)); err != nil {
			meta = json.RawMessage(c.Meta)
		}
		if _, err = args.ParseMeta(meta); err != nil {
			return output.UsageError("call", output.ArgumentsMessage(err), "--meta", "")
		}
	}
	var raw args.Raw
	switch c.ArgsFile {
	case "":
		raw, err = args.Parse(c.Assignments, c.Args, nil)
	case "-":
		raw, err = parsePayload(ctx, c.Assignments, c.Args, s.In)
	default:
		var file *os.File
		file, err = os.OpenFile(c.ArgsFile, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return callUsage("The --args-file file could not be opened.", "--args-file")
		}
		defer file.Close()
		var input io.Reader = file
		if info, e := file.Stat(); e == nil && info.Mode()&os.ModeNamedPipe != 0 {
			input = &fifoPayload{File: file, ctx: ctx}
		}
		raw, err = parsePayload(ctx, c.Assignments, c.Args, input)
	}
	if ctx.Err() != nil {
		return output.NewError("canceled", nil)
	}
	if err != nil {
		failure := output.NewError("invalid_arguments", nil)
		if errors.Is(err, args.ErrInvalidArgs) {
			// The reason names keys and the expected syntax, never a value.
			failure = output.ArgumentsError(err)
		}
		failure.NextAction = output.HelpAction("call")
		return failure
	}
	paths, err := runtimePaths()
	if err != nil {
		return err
	}
	if _, _, err = checkCallOffline(ctx, paths, c.Target); err != nil {
		return err
	}
	client, err := newRuntimeClient(opts)
	if err != nil {
		return err
	}
	client.Prompt, client.OnElicit = promptFor(ctx, s, opts, client.Paths, connection)
	waiting := startProgress(s, opts, callLabel(connection, tool))
	client.OnElicit = stopBefore(waiting, client.OnElicit)
	response, err := client.Call(ctx, runtimeclient.CallRequest{Connection: connection, Tool: tool, Arguments: raw, Timeout: timeout, Meta: meta})
	waiting.stop()
	var exportErr *output.Error
	if exports != nil && len(response.Data.Result) > 0 {
		// Not interruptible: the result is in hand and the work is bounded.
		artifacts, e := exports.Export(response.Data.Result, output.ExportPrefix(time.Now()))
		response.Data.Artifacts = artifacts
		if e != nil {
			exportErr = safeFailure(e)
			if err != nil {
				response.Data.Warnings = append(response.Data.Warnings, *exportErr)
				exportErr = nil
			}
		}
	}
	if !opts.JSON {
		for _, w := range response.Data.Warnings {
			fmt.Fprintln(s.Err, w.Message)
			if w.NextAction != "" {
				fmt.Fprintln(s.Err, w.NextAction)
			}
		}
	}
	if err != nil {
		failure := safeFailure(err)
		if failure.Code == "tool_error" || failure.Code == "input_required" {
			return &commandFailure{Data: response.Data, Failure: failure}
		}
		return failure
	}
	if exportErr != nil {
		return &commandFailure{Data: response.Data, Failure: exportErr}
	}
	return writeSuccess(s, opts, response.Data)
}

// stopBefore ends the progress line before a server prompt: the question
// takes its place. No prompt stays no prompt.
func stopBefore(p *progress, ask func(context.Context, elicit.Prompt) elicit.Answer) func(context.Context, elicit.Prompt) elicit.Answer {
	if ask == nil {
		return nil
	}
	return func(ctx context.Context, q elicit.Prompt) elicit.Answer {
		p.stop()
		return ask(ctx, q)
	}
}

// callLabel is call's progress line: the tool when its name passes the tool
// grammar, else only the connection, whose ID splitCallTarget checked.
func callLabel(connection, tool string) string {
	if output.SafeToolName(tool) {
		return "Calling " + connection + "." + tool + ellipsis()
	}
	return "Calling " + connection + ellipsis()
}

// promptFor picks where the call's server may ask its user: the terminal,
// else the opt-in native dialog, else nowhere (the daemon declines). Headless
// mode is nowhere, as if --no-input were given (D10).
func promptFor(ctx context.Context, s *Streams, opts *CommandOptions, paths config.Paths, connection string) (string, func(context.Context, elicit.Prompt) elicit.Answer) {
	if opts.NoInput || paths.Headless() {
		return "", nil
	}
	if !opts.JSON {
		in, _ := s.In.(*os.File)
		errOut, _ := s.Err.(*os.File)
		if open := newTerminal(paths, in, errOut); open != nil {
			return "terminal", func(ctx context.Context, p elicit.Prompt) elicit.Answer {
				a := elicit.Ask(ctx, open(ctx), s.Err, connection, p)
				if a.Action == "cancel" {
					// Ctrl-D, Ctrl-C and the timeout leave the cursor after the prompt.
					fmt.Fprintln(s.Err)
				}
				return a
			}
		}
	}
	state, err := config.ReadState(ctx, paths)
	if err != nil || state.Local.Runtime == nil || !state.Local.Runtime.ApprovalDialog {
		return "", nil
	}
	show := newDialogFactory(paths)
	return "dialog", func(ctx context.Context, p elicit.Prompt) elicit.Answer {
		argv := elicit.DialogArgs(connection, p)
		if argv == nil {
			return elicit.Answer{Action: "cancel"}
		}
		button, err := show(ctx, argv)
		if err != nil {
			return elicit.Answer{Action: "cancel"}
		}
		return elicit.DialogAnswer(p, strings.TrimSuffix(button, "\n"))
	}
}

func parsePayload(ctx context.Context, assignments []string, inline string, input io.Reader) (args.Raw, error) {
	if ctx.Err() != nil {
		return args.Raw{}, ctx.Err()
	}
	if file, ok := input.(*os.File); ok && file == os.Stdin {
		// Inherited stdin can be a blocking descriptor outside Go's poller. Wrap a
		// nonblocking duplicate so Close can interrupt the pending read.
		original := int(file.Fd())
		fd, err := syscall.Dup(original)
		if err != nil {
			return args.Raw{}, err
		}
		if err = syscall.SetNonblock(fd, true); err != nil {
			_ = syscall.Close(fd)
			return args.Raw{}, err
		}
		pipe := os.NewFile(uintptr(fd), file.Name())
		defer pipe.Close()
		defer func() { _ = syscall.SetNonblock(original, false) }()
		input = pipe
	}
	if closer, ok := input.(io.Closer); ok {
		done := make(chan struct{})
		stop := context.AfterFunc(ctx, func() { _ = closer.Close(); close(done) })
		defer func() {
			if !stop() {
				<-done
			}
		}()
	}
	return args.Parse(assignments, inline, input)
}

type fifoPayload struct {
	*os.File
	ctx     context.Context
	started bool
}

func (f *fifoPayload) Read(p []byte) (int, error) {
	for {
		n, err := f.File.Read(p)
		if n > 0 {
			f.started = true
		}
		if err != io.EOF || f.started {
			return n, err
		}
		select {
		case <-f.ctx.Done():
			return 0, f.ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
