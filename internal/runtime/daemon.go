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
}

var (
	errForced = errors.New("forced daemon shutdown")
	listenMu  sync.Mutex // umask is process-wide.
)

type writerGate chan struct{}

func makeWriter() writerGate { g := make(writerGate, 1); g <- struct{}{}; return g }
func writeSocket(ctx context.Context, c *net.UnixConn, g writerGate, f Frame) error {
	return writeSocketFrame(ctx, c, g, f, nil)
}

func writeSocketFrame(ctx context.Context, c *net.UnixConn, g writerGate, f Frame, beforeWrite func()) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g:
	}
	defer func() { g <- struct{}{} }()
	deadline := time.Now().Add(2 * time.Second)
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
	e := WriteFrame(c, f)
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
	dir, e := config.OpenPrivateDir(opts.Paths.RuntimeDir, false)
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
				idle := len(s.requests) == 0 && opts.Handler.Active() == 0 && !s.restarting && time.Since(s.last) >= opts.IdleTimeout
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
	s.mu.Lock()
	defer s.mu.Unlock()
	start := s.started
	return Status{Running: true, PID: os.Getpid(), ProtocolVersion: ProtocolVersion, BinaryVersion: s.opts.Version, Compatible: true, Socket: s.opts.Paths.SocketFile, Log: s.opts.Paths.LogFile, CapturedPath: s.opts.LoginEnv["PATH"], EnvFallback: s.opts.EnvFallback, ActiveCalls: len(s.requests), StartedAt: &start}
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
		writeCtx, c := context.WithTimeout(context.Background(), min(2*time.Second, s.opts.ShutdownTimeout))
		defer c()
		if ctx.Err() == nil {
			stop := context.AfterFunc(ctx, c)
			defer stop()
		}
		_ = writeSocket(writeCtx, conn, g, requestFrame("response", id, r))
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
	readerDone := make(chan struct{})
	var protocol atomic.Bool
	go func() {
		defer close(readerDone)
		f, e := ReadFrame(conn)
		if e != nil {
			cancel(context.Canceled)
			return
		}
		if validateControl(f, id) != nil {
			protocol.Store(true)
			cancel(ErrInvalidFrame)
			return
		}
		cancel(context.Canceled)
		_, e = ReadFrame(conn)
		if e == nil {
			protocol.Store(true)
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
	r := s.opts.Handler.Handle(ctx, id, req, before)
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
