package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/elicit"
	"github.com/dedene/mcparcel/internal/output"
)

type CallRequest struct {
	Connection string
	Tool       string
	Arguments  args.Raw
	Timeout    time.Duration
	Meta       json.RawMessage
}
type CallResponse struct {
	Data       output.CallData
	RequestID  string
	Dispatched bool
}
type RestartData struct {
	Restarted bool   `json:"restarted"`
	Status    Status `json:"status"`
}
type Client struct {
	Paths      config.Paths
	Version    string
	Executable string
	// Retain starts a desktop daemon from a retained copy of Executable
	// (RetainedPath); headless mode never retains.
	Retain bool
	// NoStart makes Ensure return ErrNotRunning where it would start a
	// daemon (doctor --live in an unsupervised headless runtime).
	NoStart bool
	NoInput bool
	// OnAuthURL receives the authorization URL of a running sign-in.
	OnAuthURL func(string)
	// Prompt ("terminal" or "dialog") and OnElicit let a call's server ask
	// its user; OnElicit must return once its context ends.
	Prompt   string
	OnElicit func(context.Context, elicit.Prompt) elicit.Answer
}
type LogoutData struct {
	Connection      string `json:"connection"`
	Removed         bool   `json:"removed"`
	ProviderRevoked bool   `json:"providerRevoked"`
}

func newRequestID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

func requestFrame(kind, id string, v any) Frame {
	b, _ := json.Marshal(v)
	return Frame{ProtocolVersion, kind, id, b}
}
func emptyArgs() args.Raw { return args.Raw{Values: map[string]args.Value{}} }

// ErrNotRunning is Ensure's error with NoStart when no runtime answers.
var ErrNotRunning = errors.New("the runtime is not running")

// ensureTimeout bounds Ensure, including its wait for a supervised runtime.
var ensureTimeout = 15 * time.Second

func (c *Client) Ensure(ctx context.Context) (err error) {
	caller := ctx
	defer func() {
		if caller.Err() != nil {
			err = callerError(caller)
		}
	}()
	if ctx.Err() != nil {
		return callerError(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, ensureTimeout)
	defer cancel()
	exe := c.Executable
	if exe == "" {
		var e error
		exe, e = os.Executable()
		if e != nil {
			return output.NewError("runtime_start_failed", nil)
		}
	}
	exe, e := filepath.Abs(exe)
	if e != nil {
		return output.NewError("runtime_start_failed", nil)
	}
	for {
		conn, e := dialSocket(ctx, c.Paths)
		if e == nil {
			_, e = ClientHandshake(conn, c.Version, "work", newRequestID(), configRoot(c.Paths.ConfigDir))
			_ = conn.Close()
			return e
		}
		if errors.Is(e, config.ErrUnsafePath) {
			return unsafeRuntimeDir(c.Paths, e)
		}
		if !errors.Is(e, os.ErrNotExist) && !errors.Is(e, syscall.ECONNREFUSED) && !errors.Is(e, syscall.ENOENT) {
			return output.NewError("runtime_start_failed", nil)
		}
		supervised, e := Supervised(c.Paths)
		if errors.Is(e, config.ErrUnsafePath) {
			return unsafeRuntimeDir(c.Paths, e)
		}
		if e != nil {
			return e
		}
		if ctx.Err() != nil {
			if supervised {
				return output.NewError("runtime_supervised", nil)
			}
			return output.NewError("runtime_start_failed", nil)
		}
		if supervised {
			// The supervisor starts the runtime; wait for it to answer.
			select {
			case <-ctx.Done():
			case <-time.After(25 * time.Millisecond):
			}
			continue
		}
		if c.NoStart {
			return ErrNotRunning
		}
		if _, e = startDaemon(ctx, c.Paths, exe, DaemonEnvironment(c.Paths), config.OpenPrivateFile, c.retainHook()); e != nil {
			return e
		}
		select {
		case <-ctx.Done():
			return output.NewError("runtime_start_failed", nil)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// unsafeRuntimeDir is err, an unsafe runtime directory or socket. On Linux
// in desktop mode it becomes unsafe_local_path naming MCPARCEL_RUNTIME_DIR;
// it still matches err, so config.ErrUnsafePath checks keep working.
func unsafeRuntimeDir(p config.Paths, err error) error {
	if runtimeDirAction == "" || p.Headless() {
		return err
	}
	e := output.NewError("unsafe_local_path", nil)
	e.NextAction = runtimeDirAction
	return &runtimeDirError{safe: e, cause: err}
}

// runtimeDirError is the safe unsafe_local_path error of an unsafe runtime
// directory together with its cause.
type runtimeDirError struct {
	safe  *output.Error
	cause error
}

func (e *runtimeDirError) Error() string   { return e.safe.Error() }
func (e *runtimeDirError) Unwrap() []error { return []error{e.safe, e.cause} }

func (c *Client) exchange(ctx context.Context, intent string, r Request, ensure bool) (Response, string, error) {
	return c.exchangeAck(ctx, intent, r, ensure, nil)
}

// handshakeInfo records how far an exchange got: whether it connected, and
// the handshake's ack and error, which exchange itself does not return.
type handshakeInfo struct {
	connected bool
	ack       HelloAck
	err       error
}

func (c *Client) exchangeAck(ctx context.Context, intent string, r Request, ensure bool, info *handshakeInfo) (Response, string, error) {
	if info == nil {
		info = &handshakeInfo{}
	}
	var out Response
	id := newRequestID()
	if id == "" {
		return out, id, output.NewError("internal_error", nil)
	}
	if ensure {
		if e := c.Ensure(ctx); e != nil {
			return out, id, e
		}
	}
	conn, e := dialSocket(ctx, c.Paths)
	if e != nil {
		switch {
		case ctx.Err() != nil:
			e = callerError(ctx)
		case errors.Is(e, config.ErrUnsafePath):
			e = unsafeRuntimeDir(c.Paths, e)
		}
		return out, id, e
	}
	defer func() { _ = conn.Close() }()
	info.connected = true
	watchDone := make(chan struct{})
	watchExited := make(chan struct{})
	go func() {
		defer close(watchExited)
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Now())
		case <-watchDone:
		}
	}()
	info.ack, e = ClientHandshake(conn, c.Version, intent, id, configRoot(c.Paths.ConfigDir))
	info.err = e
	close(watchDone)
	<-watchExited
	if ctx.Err() != nil {
		return out, id, callerError(ctx)
	}
	if e != nil {
		return out, id, e
	}
	attempted := false
	if e = writeSocketFrame(ctx, conn, makeWriter(), requestFrame("request", id, r), func() { attempted = r.Method == "call" }); e != nil {
		return c.lost(ctx, id, attempted)
	}
	type readResult struct {
		f Frame
		e error
	}
	ch := make(chan readResult, 1)
	readerStop := make(chan struct{})
	defer close(readerStop)
	go func() {
		for {
			f, e := readFrame(conn, MaxResponseFrameBytes)
			select {
			case ch <- readResult{f, e}:
			case <-readerStop:
				return
			}
			if e != nil || f.Kind == "response" {
				return
			}
		}
	}()
	var asked *prompts
	var answers <-chan ElicitAnswer
	if r.Prompt != "" && c.OnElicit != nil {
		asked = newPrompts(ctx, c.OnElicit)
		answers = asked.answers
		defer func() { asked.stop() }()
	}
	cancelCh := ctx.Done()
	var grace *time.Timer
	var graceCh <-chan time.Time
	defer func() {
		if grace != nil {
			grace.Stop()
		}
	}()
	for {
		select {
		case <-cancelCh:
			if r.Method == "status" {
				return out, id, callerError(ctx)
			}
			cancelCh = nil
			cancelDeadline := time.Now().Add(time.Second)
			cancelCtx, stop := context.WithDeadline(context.Background(), cancelDeadline)
			_ = writeSocket(cancelCtx, conn, makeWriter(), requestFrame("cancel", id, struct{}{}))
			stop()
			grace = time.NewTimer(max(0, time.Until(cancelDeadline)))
			graceCh = grace.C
			if asked != nil {
				asked.stop()
			}
		case a := <-answers:
			if cancelCh != nil {
				_ = writeSocket(ctx, conn, makeWriter(), requestFrame("elicit_answer", id, a))
			}
		case <-graceCh:
			return out, id, output.NewError("canceled", &output.Details{RequestID: id, Dispatched: attempted, Outcome: outcome(attempted)})
		case v := <-ch:
			if v.e != nil {
				return c.lost(ctx, id, attempted)
			}
			if v.f.ProtocolVersion != ProtocolVersion || v.f.RequestID != id {
				return c.lost(ctx, id, attempted)
			}
			if v.f.Kind == "dispatch" && r.Method == "call" {
				out.Dispatched = true
				continue
			}
			if v.f.Kind == "elicit" && asked != nil && out.Dispatched {
				var e Elicit
				if decodeBody(v.f.Body, &e) != nil {
					return c.lost(ctx, id, attempted)
				}
				if cancelCh != nil {
					asked.start(e)
				}
				continue
			}
			if v.f.Kind == "auth_url" && r.Method == "auth" {
				var u AuthURL
				if decodeBody(v.f.Body, &u) != nil {
					return c.lost(ctx, id, attempted)
				}
				if c.OnAuthURL != nil {
					c.OnAuthURL(u.URL)
				}
				continue
			}
			if v.f.Kind != "response" {
				return c.lost(ctx, id, attempted)
			}
			if e = decodeBody(v.f.Body, &out); e != nil {
				return c.lost(ctx, id, attempted)
			}
			if out.Error != nil {
				return out, id, out.Error
			}
			return out, id, nil
		}
	}
}

func outcome(dispatched bool) string {
	if dispatched {
		return "unknown"
	}
	return ""
}

func (c *Client) lost(ctx context.Context, id string, attempted bool) (Response, string, error) {
	code := "connection_failed"
	if attempted {
		code = "outcome_unknown"
	}
	if ctx.Err() != nil {
		code = "canceled"
	}
	return Response{Dispatched: attempted}, id, output.NewError(code, &output.Details{RequestID: id, Dispatched: attempted, Outcome: outcome(attempted)})
}

func (c *Client) Tools(ctx context.Context, id string, cached bool) (output.ToolList, error) {
	var out output.ToolList
	if cached {
		return out, output.NewError("schema_cache_miss", nil)
	}
	r, _, e := c.exchange(ctx, "work", Request{Method: "tools", Connection: id, Arguments: emptyArgs(), NoInput: c.NoInput}, true)
	if e == nil {
		e = decodeBody(r.Data, &out)
	}
	return out, e
}

func (c *Client) Call(ctx context.Context, req CallRequest) (CallResponse, error) {
	r := Request{Method: "call", Connection: req.Connection, Tool: req.Tool, Arguments: req.Arguments, NoInput: c.NoInput, Meta: req.Meta}
	if c.OnElicit != nil && !c.NoInput {
		r.Prompt = c.Prompt
	}
	if req.Timeout > 0 {
		r.Timeout = req.Timeout.String()
	}
	resp, id, e := c.exchange(ctx, "work", r, true)
	out := CallResponse{RequestID: id, Dispatched: resp.Dispatched}
	if resp.Data != nil {
		if de := decodeBody(resp.Data, &out.Data); de != nil && e == nil {
			e = de
		}
	}
	if len(out.Data.Artifacts) > 0 {
		// Only the CLI saves files; a daemon naming artifacts is broken or hostile.
		return CallResponse{RequestID: id, Dispatched: resp.Dispatched}, output.NewError("protocol_error", &output.Details{RequestID: id, Dispatched: resp.Dispatched, Outcome: outcome(resp.Dispatched)})
	}
	return out, e
}

// Auth makes a connection's credentials fresh through the runtime, which it
// starts if needed. name is what the user typed; messages use it.
func (c *Client) Auth(ctx context.Context, name string) (AuthData, error) {
	var out AuthData
	r, _, e := c.exchange(ctx, "work", Request{Method: "auth", Connection: name, Arguments: emptyArgs(), NoInput: c.NoInput}, true)
	if e == nil {
		e = decodeBody(r.Data, &out)
	}
	return out, e
}

func (c *Client) Logout(ctx context.Context, canonical string) (LogoutData, error) {
	var out LogoutData
	r, _, e := c.exchange(ctx, "work", Request{Method: "logout", Connection: canonical, Arguments: emptyArgs()}, true)
	if e == nil {
		e = decodeBody(r.Data, &out)
	}
	return out, e
}

// Lock ends every credential session; like Logout it starts the daemon, which
// is the only writer of the OAuth lock.
func (c *Client) Lock(ctx context.Context) (LockData, error) {
	var out LockData
	r, _, e := c.exchange(ctx, "work", Request{Method: "lock", Arguments: emptyArgs()}, true)
	if e == nil {
		e = decodeBody(r.Data, &out)
	}
	return out, e
}

type StopData struct {
	Stopped    bool `json:"stopped"`
	WasRunning bool `json:"wasRunning"`
}

func (c *Client) Stop(ctx context.Context, force bool) (StopData, error) {
	return c.stop(ctx, "stop", force)
}

func (c *Client) stop(ctx context.Context, intent string, force bool) (StopData, error) {
	out := StopData{Stopped: true}
	r, _, e := c.exchange(ctx, intent, Request{Method: intent, Force: force, Arguments: emptyArgs()}, false)
	if e != nil && !errors.Is(e, os.ErrNotExist) && !errors.Is(e, syscall.ECONNREFUSED) {
		return StopData{}, e
	}
	if e == nil {
		if e = decodeBody(r.Data, &out); e != nil || !out.Stopped {
			return StopData{}, output.NewError("protocol_error", nil)
		}
		out.WasRunning = true
	}
	join, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if e = waitLock(join, c.Paths); e != nil {
		return StopData{}, e
	}
	return out, nil
}

func (c *Client) Restart(ctx context.Context, force bool) (RestartData, error) {
	var out RestartData
	_, e := c.stop(ctx, "restart", force)
	if e != nil {
		return out, e
	}
	if e = c.Ensure(ctx); e != nil {
		return out, e
	}
	out.Status, e = c.Status(ctx)
	out.Restarted = e == nil
	return out, e
}

func callerError(ctx context.Context) *output.Error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return output.NewError("canceled", nil)
	}
	return output.NewError("runtime_start_failed", nil)
}
