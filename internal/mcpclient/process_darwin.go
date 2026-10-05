package mcpclient

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

func Executable(command, path, cwd string) (string, error) {
	valid := func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
	}
	if strings.ContainsRune(command, '/') {
		if filepath.IsAbs(command) && valid(command) {
			return command, nil
		}
		return "", output.NewError("connection_failed", nil)
	}
	if command != "" {
		for _, dir := range filepath.SplitList(path) {
			if !filepath.IsAbs(dir) {
				continue
			}
			candidate := filepath.Join(dir, command)
			if valid(candidate) {
				return candidate, nil
			}
		}
	}
	return "", output.NewError("connection_failed", nil)
}

type ownedProcess struct {
	stdin  io.WriteCloser
	cmd    *exec.Cmd
	done   chan struct{}
	closed chan struct{}
	once   sync.Once
	grace  time.Duration
}

func (p *ownedProcess) Write(data []byte) (int, error) { return p.stdin.Write(data) }
func (p *ownedProcess) Close() error {
	p.once.Do(func() {
		_ = p.stdin.Close()
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(p.grace)
		defer timer.Stop()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		group := -p.cmd.Process.Pid
		for syscall.Kill(group, 0) != syscall.ESRCH {
			select {
			case <-timer.C:
				_ = syscall.Kill(group, syscall.SIGKILL)
				<-p.done
				close(p.closed)
				return
			case <-tick.C:
			}
		}
		<-p.done
		close(p.closed)
	})
	<-p.closed
	return nil
}

func startProcess(opts ConnectOptions) (mcp.Transport, func(context.Context), error) {
	c := opts.Connection.Transport.Stdio
	cwd := opts.Home
	if c.Cwd != nil {
		var err error
		cwd, err = config.LiteralText(*c.Cwd)
		if err != nil {
			return nil, nil, output.NewError("config_required", nil)
		}
	}
	text, err := config.LiteralText(c.Command)
	if err != nil {
		return nil, nil, output.NewError("config_required", nil)
	}
	args := make([]string, len(c.Args))
	for i, v := range c.Args {
		args[i], err = config.LiteralText(v)
		if err != nil {
			return nil, nil, output.NewError("config_required", nil)
		}
	}
	command, err := Executable(text, opts.Env["PATH"], cwd)
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(command, args...)
	cmd.Dir = cwd
	cmd.Env = make([]string, 0, len(opts.Env))
	for k, v := range opts.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	sort.Strings(cmd.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, output.NewError("connection_failed", nil)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, nil, output.NewError("connection_failed", nil)
	}
	if err = cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, nil, output.NewError("connection_failed", nil)
	}
	p := &ownedProcess{stdin: stdin, cmd: cmd, done: make(chan struct{}), closed: make(chan struct{}), grace: opts.ShutdownTimeout}
	go func() { _ = cmd.Wait(); close(p.done) }()
	return &pipeTransport{IOTransport: &mcp.IOTransport{Reader: stdout, Writer: p, MaxLineLength: 16 * 1024 * 1024}, writer: stdin.(*os.File)}, func(ctx context.Context) { _ = p.CloseContext(ctx); _ = stdout.Close() }, nil
}

type pipeTransport struct {
	*mcp.IOTransport
	writer *os.File
}

func (t *pipeTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	c, err := t.IOTransport.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &pipeConnection{Connection: c, writer: t.writer}, nil
}

type pipeConnection struct {
	mcp.Connection
	writer *os.File
	mu     sync.Mutex
}

func (c *pipeConnection) Write(ctx context.Context, msg jsonrpc.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = c.writer.SetWriteDeadline(time.Now()); close(done) })
	err := c.Connection.Write(ctx, msg)
	if !stop() {
		<-done
	}
	_ = c.writer.SetWriteDeadline(time.Time{})
	return err
}

func (p *ownedProcess) CloseContext(ctx context.Context) error {
	select {
	case <-p.closed:
		return nil
	default:
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); close(done) })
	err := p.Close()
	if !stop() {
		<-done
	}
	return err
}
