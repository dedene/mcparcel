package cmd

import (
	"context"
	"io"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
)

type CallCmd struct {
	Target      string   `arg:"" required:"" help:"Connection ID and tool name, separated by a dot."`
	Assignments []string `arg:"" optional:"" sep:"none" help:"Arguments as key=value, key:value or key:=json."`
	Args        string   `name:"args" help:"Arguments as one JSON object."`
	ArgsFile    string   `name:"args-file" help:"Read a JSON object from a file, or - for stdin."`
	Timeout     string   `name:"timeout" help:"Positive call duration, including queue and initialization."`
}

var connectionID = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func splitCallTarget(target string) (connection, tool string, err error) {
	offset := 0
	if strings.HasPrefix(target, "github:") {
		hash := strings.IndexByte(target, '#')
		if hash < 0 {
			return "", "", output.NewError("invalid_arguments", nil)
		}
		offset = hash + 1
	}
	index := strings.IndexByte(target[offset:], '.')
	if index < 0 {
		return "", "", output.NewError("invalid_arguments", nil)
	}
	index += offset
	connection, tool = target[:index], target[index+1:]
	valid := connectionID.MatchString(connection)
	if strings.Contains(connection, ":") {
		valid = config.ValidateCanonicalID(connection) == nil
	}
	if !valid || tool == "" {
		return "", "", output.NewError("invalid_arguments", nil)
	}
	return connection, tool, nil
}

func validateCallSources(c *CallCmd, flags map[string]int) error {
	sources := 0
	for _, flag := range []string{"--args", "--args-file"} {
		if flags[flag] > 1 {
			return output.NewError("invalid_arguments", nil)
		}
		if flags[flag] > 0 {
			sources++
		}
	}
	if len(c.Assignments) > 0 {
		sources++
	}
	if sources > 1 || flags["--args"] > 0 && c.Args == "" || flags["--args-file"] > 0 && c.ArgsFile == "" {
		return output.NewError("invalid_arguments", nil)
	}
	if flags["--timeout"] > 0 {
		d, err := time.ParseDuration(c.Timeout)
		if err != nil || d <= 0 {
			return output.NewError("invalid_arguments", nil)
		}
	}
	return nil
}

func (c *CallCmd) Run(ctx context.Context, s *Streams, opts *CommandOptions) error {
	connection, tool, err := splitCallTarget(c.Target)
	if err != nil {
		return err
	}
	var timeout time.Duration
	if c.Timeout != "" {
		timeout, err = time.ParseDuration(c.Timeout)
		if err != nil || timeout <= 0 {
			return output.NewError("invalid_arguments", nil)
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
			return output.NewError("invalid_arguments", nil)
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
		return output.NewError("invalid_arguments", nil)
	}
	client, err := newRuntimeClient(opts)
	if err != nil {
		return err
	}
	response, err := client.Call(ctx, runtimeclient.CallRequest{Connection: connection, Tool: tool, Arguments: raw, Timeout: timeout})
	if err != nil {
		failure := safeFailure(err)
		if failure.Code == "tool_error" || failure.Code == "input_required" {
			return &commandFailure{Data: response.Data, Failure: failure}
		}
		return failure
	}
	return writeSuccess(s, opts, response.Data)
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
