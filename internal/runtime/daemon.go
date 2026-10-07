package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/elicit"
	"github.com/dedene/mcparcel/internal/mcpclient"
	"github.com/dedene/mcparcel/internal/output"
)

type DaemonOptions struct {
	Paths           config.Paths
	Version         string
	Lock            *os.File
	LoginEnv        map[string]string
	EnvFallback     bool
	Handler         Handler
	Log             io.Writer
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	// PromptTimeout backs up the CLI's own elicit.PromptTimeout; set only in tests.
	PromptTimeout time.Duration
}

var (
	errForced = errors.New("forced daemon shutdown")
	listenMu  sync.Mutex // umask is process-wide.
)

type authURLKey struct{}

// withAuthURLSender lets a login stream its authorization URL to the CLI.
func withAuthURLSender(ctx context.Context, send func(string) error) context.Context {
	return context.WithValue(ctx, authURLKey{}, send)
}

// authURLSender returns the login's URL sender, or nil outside a login.
func authURLSender(ctx context.Context) func(string) error {
	send, _ := ctx.Value(authURLKey{}).(func(string) error)
	return send
}

// writeDeadline bounds one frame write to a peer that stops reading: 2s, plus
// 1s per 4 MiB so a large result survives a briefly busy CLI.
func writeDeadline(size int) time.Duration {
	return 2*time.Second + time.Duration(size/(4<<20))*time.Second
}

type writerGate chan struct{}

func makeWriter() writerGate { g := make(writerGate, 1); g <- struct{}{}; return g }
func writeSocket(ctx context.Context, c *net.UnixConn, g writerGate, f Frame) error {
	return writeSocketFrame(ctx, c, g, f, nil)
}

func writeSocketFrame(ctx context.Context, c *net.UnixConn, g writerGate, f Frame, beforeWrite func()) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	buf, e := encodeFrame(f)
	if e != nil {
		return e
	}
	return writeSocketBytes(ctx, c, g, buf, beforeWrite)
}

// writeSocketBytes writes one encoded frame. Encoding a large result takes
// time of its own, so callers encode before their write budget starts.
func writeSocketBytes(ctx context.Context, c *net.UnixConn, g writerGate, buf []byte, beforeWrite func()) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g:
	}
	defer func() { g <- struct{}{} }()
	deadline := time.Now().Add(writeDeadline(len(buf)))
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.SetWriteDeadline(deadline)
	done, exited := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		select {
		case <-ctx.Done():
			_ = c.SetWriteDeadline(time.Now())
		case <-done:
		}
	}()
	if beforeWrite != nil {
		beforeWrite()
	}
	e := writeEncoded(c, buf)
	close(done)
	<-exited
	_ = c.SetWriteDeadline(time.Time{})
	return e
}

type daemonService struct {
	opts        DaemonOptions
	listener    *net.UnixListener
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	admitting   bool
	restarting  bool
	requests    map[string]context.CancelCauseFunc
	last        time.Time
	started     time.Time
	work        sync.WaitGroup
	sockets     sync.WaitGroup
	shutdown    sync.Once
	shutdownErr error
}

func Serve(ctx context.Context, opts DaemonOptions) error {
	if opts.Lock == nil || opts.Handler == nil {
		return config.ErrUnsafePath
	}
	defer func() { _ = opts.Lock.Close() }()
	// Validate the supplied lifetime descriptor without replacing it.
	dup, e := unix.Dup(int(opts.Lock.Fd()))
	if e != nil {
		return config.ErrUnsafePath
	}
	validated, e := AdoptLock(opts.Paths, uintptr(dup))
	if e != nil {
		return e
	}
	_ = validated.Close()
	dir, e := config.OpenPrivateDirUnder(opts.Paths.StateRoot, opts.Paths.RuntimeDir, false)
	if e != nil {
		return e
	}
	defer func() { _ = dir.Close() }()
	st, e := socketStat(dir)
	if e != nil {
		return e
	}
	if st != nil {
		return config.ErrUnsafePath
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 24 * time.Hour
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = 5 * time.Second
	}
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	if opts.PromptTimeout <= 0 {
		opts.PromptTimeout = elicit.PromptTimeout + 5*time.Second
	}
	listenMu.Lock()
	mask := unix.Umask(0o177)
	l, e := net.ListenUnix("unix", &net.UnixAddr{Name: opts.Paths.SocketFile, Net: "unix"})
	unix.Umask(mask)
	listenMu.Unlock()
	if e != nil {
		return output.NewError("runtime_start_failed", nil)
	}
	l.SetUnlinkOnClose(false)
	owned, e := socketStat(dir)
	if e != nil {
		_ = l.Close()
		return e
	}
	life, cancel := context.WithCancel(ctx)
	s := &daemonService{opts: opts, listener: l, ctx: life, cancel: cancel, admitting: true, requests: map[string]context.CancelCauseFunc{}, last: time.Now(), started: time.Now()}
	path := opts.LoginEnv["PATH"]
	_ = WriteLog(opts.Log, "daemon_started", &path)
	if opts.EnvFallback {
		_ = WriteLog(opts.Log, "login_env_fallback", nil)
	}
	keepDone := make(chan struct{})
	if k, ok := opts.Handler.(interface{ RunKeepAlive(context.Context) error }); ok {
		go func() { defer close(keepDone); _ = k.RunKeepAlive(life) }()
	} else {
		close(keepDone)
	}
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		ticker := time.NewTicker(min(opts.IdleTimeout, 100*time.Millisecond))
		defer ticker.Stop()
		for {
			select {
			case <-life.Done():
				_ = l.Close()
				return
			case <-ticker.C:
				s.mu.Lock()
				idle := s.idleLocked()
				s.mu.Unlock()
				if !idle {
					continue
				}
				// Stay-alive reads the config, so it runs outside s.mu.
				if s.stayAlive() {
					s.mu.Lock()
					s.last = time.Now()
					s.mu.Unlock()
					continue
				}
				s.mu.Lock()
				idle = s.idleLocked()
				if idle {
					s.admitting = false
				}
				s.mu.Unlock()
				if idle {
					cancel()
				}
			}
		}
	}()
	for {
		conn, e := l.AcceptUnix()
		if e != nil {
			break
		}
		s.sockets.Go(func() { s.serveSocket(conn) })
	}
	cancel()
	s.stop(true)
	s.sockets.Wait()
	<-watcherDone
	// A refresh still running was waited for by the handler's shutdown.
	<-keepDone
	current, e := socketStat(dir)
	if e == nil && sameSocket(owned, current) {
		if e = unix.Unlinkat(int(dir.Fd()), "daemon.sock", 0); e != nil {
			return config.ErrUnsafePath
		}
	}
	_ = WriteLog(opts.Log, "daemon_stopped", nil)
	return s.shutdownErr
}

func (s *daemonService) status() Status {
	stay := s.stayAlive()
	s.mu.Lock()
	defer s.mu.Unlock()
	start := s.started
	return Status{Running: true, PID: os.Getpid(), ProtocolVersion: ProtocolVersion, BinaryVersion: s.opts.Version, Compatible: true, Socket: s.opts.Paths.SocketFile, Log: s.opts.Paths.LogFile, CapturedPath: s.opts.LoginEnv["PATH"], EnvFallback: s.opts.EnvFallback, ActiveCalls: len(s.requests), StayAlive: stay, StartedAt: &start}
}

// idleLocked reports an idle daemon; s.mu is held.
func (s *daemonService) idleLocked() bool {
	return len(s.requests) == 0 && s.opts.Handler.Active() == 0 && !s.restarting && time.Since(s.last) >= s.opts.IdleTimeout
}

// stayAlive asks the handler whether runtime.keepAlive should skip the idle
// exit; never call it with s.mu held.
func (s *daemonService) stayAlive() bool {
	h, ok := s.opts.Handler.(interface{ StayAlive() bool })
	return ok && h.StayAlive()
}

func (s *daemonService) stop(force bool) {
	s.shutdown.Do(func() {
		ctx, c := context.WithTimeout(context.Background(), s.opts.ShutdownTimeout)
		defer c()
		s.mu.Lock()
		s.admitting = false
		for _, cancel := range s.requests {
			cancel(errForced)
		}
		s.mu.Unlock()
		s.shutdownErr = s.opts.Handler.Shutdown(ctx, force)
		done := make(chan struct{})
		go func() { s.work.Wait(); close(done) }()
		select {
		case <-done:
		case <-ctx.Done():
			s.shutdownErr = output.NewError("runtime_start_failed", nil)
		}
	})
}

// writeResponse writes an encoded response under the size-scaled write
// deadline. While the request is live a shutdown cancels the write through
// ctx; a response written after ctx ended is capped at the shutdown timeout,
// so it cannot hold a shutdown.
func (s *daemonService) writeResponse(ctx context.Context, conn *net.UnixConn, g writerGate, buf []byte) error {
	budget := writeDeadline(len(buf))
	if ctx.Err() != nil {
		budget = min(budget, s.opts.ShutdownTimeout)
	}
	writeCtx, c := context.WithTimeout(context.Background(), budget)
	defer c()
	if ctx.Err() == nil {
		stop := context.AfterFunc(ctx, c)
		defer stop()
	}
	return writeSocketBytes(writeCtx, conn, g, buf, nil)
}

func (s *daemonService) serveSocket(conn *net.UnixConn) {
	defer func() { _ = conn.Close() }()
	hello, id, e := ServerHandshake(conn, s.opts.Version, os.Getpid(), configRoot(s.opts.Paths.ConfigDir))
	if e != nil {
		return
	}
	g := makeWriter()
	ctx, cancel := context.WithCancelCause(s.ctx)
	defer cancel(nil)
	// Bound the request read as well: a handshake-only client cannot retain shutdown.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	f, e := ReadFrame(conn)
	_ = conn.SetReadDeadline(time.Time{})
	send := func(r Response) {
		// Encode before the write budget starts: a large result must not spend it.
		buf, e := encodeFrame(requestFrame("response", id, r))
		if e != nil {
			return
		}
		_ = s.writeResponse(ctx, conn, g, buf)
	}
	if e != nil {
		if !errors.Is(e, io.EOF) {
			send(Response{Error: output.NewError("protocol_error", nil)})
		}
		return
	}
	var req Request
	if f.ProtocolVersion != ProtocolVersion || f.RequestID != id || f.Kind != "request" || decodeBody(f.Body, &req) != nil || validateRequest(hello.Intent, req) != nil {
		send(Response{Error: output.NewError("protocol_error", nil)})
		return
	}
	s.mu.Lock()
	s.last = time.Now()
	s.mu.Unlock()
	if req.Method == "status" {
		b, _ := json.Marshal(s.status())
		send(Response{Data: b})
		return
	}
	if req.Method == "restart" || req.Method == "stop" {
		s.mu.Lock()
		if s.restarting || !s.admitting {
			s.mu.Unlock()
			send(Response{Error: output.NewError("runtime_busy", nil)})
			return
		}
		s.admitting = false
		if !req.Force && (len(s.requests) > 0 || s.opts.Handler.Active() > 0) {
			s.admitting = true
			s.mu.Unlock()
			send(Response{Error: output.NewError("runtime_busy", nil)})
			return
		}
		s.restarting = true
		s.mu.Unlock()
		s.stop(req.Force)
		if s.shutdownErr != nil {
			send(Response{Error: output.NewError("runtime_start_failed", nil)})
		} else {
			send(Response{Data: json.RawMessage(`{"stopped":true}`)})
		}
		s.cancel()
		_ = s.listener.Close()
		return
	}
	s.mu.Lock()
	if !s.admitting {
		s.mu.Unlock()
		send(Response{Error: output.NewError("connection_failed", nil)})
		return
	}
	// Socket identity, rather than client-controlled ID, owns cancellation tracking.
	key := fmt.Sprintf("%p", conn)
	s.requests[key] = cancel
	s.work.Add(1)
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.requests, key); s.last = time.Now(); s.mu.Unlock(); s.work.Done() }()
	var prompter *socketPrompter
	if req.Method == "call" && req.Prompt != "" {
		prompter = newSocketPrompter(ctx, func(c context.Context, e Elicit) error {
			return writeSocket(c, conn, g, requestFrame("elicit", id, e))
		}, s.opts.PromptTimeout)
	}
	readerDone := make(chan struct{})
	var protocol atomic.Bool
	go func() {
		defer close(readerDone)
		canceled := false
		for {
			f, e := ReadFrame(conn)
			if e != nil {
				cancel(context.Canceled)
				return
			}
			var a ElicitAnswer
			switch {
			case validateControl(f, id, "cancel") == nil && !canceled:
				canceled = true
				cancel(context.Canceled)
			case validateControl(f, id, "elicit_answer") == nil && prompter != nil && !canceled && decodeBody(f.Body, &a) == nil:
				prompter.deliver(a)
			default:
				protocol.Store(true)
				cancel(ErrInvalidFrame)
				return
			}
		}
	}()
	before := func() error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if e := writeSocket(ctx, conn, g, requestFrame("dispatch", id, struct{}{})); e != nil {
			return e
		}
		return nil
	}
	handleCtx := ctx
	if prompter != nil {
		handleCtx = mcpclient.WithPrompter(ctx, &mcpclient.Prompter{Ask: prompter.ask, Forms: req.Prompt == "terminal"})
	}
	if req.Method == "login" {
		handleCtx = withAuthURLSender(ctx, func(u string) error {
			return writeSocket(ctx, conn, g, requestFrame("auth_url", id, AuthURL{URL: u}))
		})
	}
	r := s.opts.Handler.Handle(handleCtx, id, req, before)
	if protocol.Load() {
		r = Response{Error: output.NewError("protocol_error", &output.Details{RequestID: id, Dispatched: r.Dispatched, Outcome: outcome(r.Dispatched)}), Dispatched: r.Dispatched}
	}
	if errors.Is(context.Cause(ctx), errForced) {
		code := "canceled"
		if r.Dispatched {
			code = "outcome_unknown"
		}
		r.Error = output.NewError(code, &output.Details{RequestID: id, Dispatched: r.Dispatched, Outcome: outcome(r.Dispatched)})
		r.Data = nil
	}
	send(r)
	_ = conn.Close()
	<-readerDone
}
