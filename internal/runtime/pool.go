package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/dedene/mcparcel/internal/args"
	"github.com/dedene/mcparcel/internal/auth"
	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/mcpclient"
	"github.com/dedene/mcparcel/internal/output"
)

type PoolOptions struct {
	Paths           config.Paths
	LoginEnv        map[string]string
	Version         string
	Credentials     auth.Resolver
	Log             func(event string)
	SignInFailure   func(stage, code string) // logs a failed sign-in; see WriteSignInFailure
	Keychain        func(ctx context.Context, name string) (string, error)
	Keyring         auth.Keyring // OAuth sessions; nil fails marked connections with keychain_unavailable
	Load            func(config.Paths) (config.Snapshot, error)
	Connect         func(context.Context, mcpclient.ConnectOptions) (mcpclient.Session, error)
	Now             func() time.Time
	ConnectTimeout  time.Duration
	ShutdownTimeout time.Duration
}
type pool struct {
	opts     PoolOptions
	mu       sync.Mutex
	closed   bool
	gates    map[string]chan struct{}
	entries  map[string]*poolEntry
	requests map[*poolWork]context.CancelCauseFunc
	workers  sync.WaitGroup
	expiry   sync.WaitGroup
	stopOnce sync.Once
	stopDone chan struct{}
	stopErr  error
}
type poolWork struct{ ctx context.Context }

func NewPool(opts PoolOptions) Handler {
	if opts.Load == nil {
		opts.Load = config.Load
	}
	if opts.Connect == nil {
		opts.Connect = mcpclient.Connect
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Log == nil {
		opts.Log = func(string) {}
	}
	if opts.SignInFailure == nil {
		opts.SignInFailure = func(string, string) {}
	}
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = 30 * time.Second
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = 5 * time.Second
	}
	login := make(map[string]string, len(opts.LoginEnv))
	for k, v := range opts.LoginEnv {
		login[k] = v
	}
	opts.LoginEnv = login
	return &pool{opts: opts, gates: map[string]chan struct{}{}, entries: map[string]*poolEntry{}, requests: map[*poolWork]context.CancelCauseFunc{}, stopDone: make(chan struct{})}
}
func (p *pool) Active() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.requests) }
func (p *pool) Handle(ctx context.Context, id string, req Request, before func() error) (resp Response) {
	started := time.Now()
	workCtx, cancel := context.WithCancelCause(ctx)
	w := &poolWork{ctx: workCtx}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		cancel(nil)
		return Response{Error: output.NewError("connection_failed", nil)}
	}
	if req.Method == "tools" && req.Cached {
		p.mu.Unlock()
		cancel(nil)
		return Response{Error: output.NewError("schema_cache_miss", nil)}
	}
	p.requests[w] = cancel
	p.workers.Add(1)
	p.mu.Unlock()
	defer func() { cancel(nil); p.mu.Lock(); delete(p.requests, w); p.mu.Unlock(); p.workers.Done() }()
	fail := func(e error) Response { return Response{Error: poolError(e, context.Cause(workCtx), id, false)} }
	if req.Method != "call" && req.Method != "tools" && req.Method != "login" && req.Method != "logout" {
		return fail(output.NewError("protocol_error", nil))
	}
	if req.Method == "logout" {
		return p.logout(workCtx, req.Connection)
	}
	if p.opts.Credentials == nil {
		return fail(errors.New("missing credentials resolver"))
	}
	snapshot, e := p.opts.Load(p.opts.Paths)
	if e != nil {
		return fail(e)
	}
	canonical, c, e := snapshot.RuntimeConnection(req.Connection)
	if e != nil {
		return fail(e)
	}
	if req.Method == "call" {
		if e = snapshot.CheckTool(canonical, req.Tool); e != nil {
			return fail(e)
		}
	}
	var login *auth.LoginOptions
	if req.Method == "login" {
		if login, e = loginOptions(ctx, req, c); e != nil {
			return fail(e)
		}
	}
	hash, e := snapshot.ConnectionHash(canonical)
	if e != nil {
		return fail(e)
	}
	duration := 180 * time.Second
	if req.Method == "login" {
		duration = loginTimeout
	}
	if req.Method == "call" {
		duration = 120 * time.Second
		text := req.Timeout
		if text == "" {
			text = c.CallTimeout
		}
		if text != "" {
			duration, e = time.ParseDuration(text)
			if e != nil || duration <= 0 {
				return fail(args.ErrInvalidArgs)
			}
		}
	}
	deadline, deadlineCancel := withCallDeadline(workCtx, time.Until(started.Add(duration)))
	defer deadlineCancel()
	workCtx = deadline
	gate := p.gate(canonical)
	if workCtx.Err() != nil {
		return fail(workCtx.Err())
	}
	select {
	case <-workCtx.Done():
		return fail(workCtx.Err())
	case <-gate:
	}
	defer func() { gate <- struct{}{} }()
	if workCtx.Err() != nil {
		return fail(workCtx.Err())
	}
	snapshot, e = p.opts.Load(p.opts.Paths)
	if e != nil {
		return fail(e)
	}
	_, c, e = snapshot.RuntimeConnection(canonical)
	if e != nil {
		return fail(e)
	}
	if req.Method == "call" {
		if e = snapshot.CheckTool(canonical, req.Tool); e != nil {
			return fail(e)
		}
	}
	hash, e = snapshot.ConnectionHash(canonical)
	if e != nil {
		return fail(e)
	}
	lease := auth.Lease{Identity: "public"}
	refs := config.SecretRefs(c)
	if oauthCapable(c) && len(refs) == 0 {
		defer func() {
			if resp.Error != nil && resp.Error.Code == "auth_required" {
				resp.Error.NextAction = "mcparcel auth login " + req.Connection
				if resp.Error.Message == output.NewError("auth_required", nil).Message {
					resp.Error.Message = "Sign-in required for " + req.Connection + "."
				}
				if c.Auth == nil {
					p.rememberSignIn(ctx, canonical, c)
				}
			}
		}()
	} else if names := config.EnvRefs(c); len(names) > 0 && len(refs) == 0 {
		defer func() {
			if resp.Error != nil && resp.Error.Code == "auth_required" {
				resp.Error.NextAction = "Check " + strings.Join(names, ", ") + " (login-shell environment, else the Keychain generic password of the same name), update the value, then run mcparcel runtime restart."
			}
		}()
	}
	if len(refs) > 0 {
		authCtx, authCancel := context.WithTimeout(workCtx, 120*time.Second)
		lease, e = p.opts.Credentials.Resolve(authCtx, c.CredentialProfile, snapshot.Local.CredentialProfiles[c.CredentialProfile], refs, req.NoInput)
		authCancel()
		if e != nil {
			p.opts.Log("auth_failed")
			return fail(e)
		}
		if !p.opts.Now().Before(lease.SessionExpiresAt) {
			return fail(auth.ErrExpired)
		}
	}
	entry, handler, e := p.session(workCtx, canonical, hash, c, lease, gate, req.Connection, login)
	if login != nil {
		return p.finishLogin(workCtx, req.Connection, canonical, entry, handler, e, fail)
	}
	if e != nil {
		return fail(e)
	}
	callCtx, callCancel := context.WithCancelCause(workCtx)
	defer callCancel(nil)
	detach := context.AfterFunc(entry.ctx, func() { callCancel(context.Cause(entry.ctx)) })
	defer detach()
	if entry.ctx.Err() != nil {
		callCancel(context.Cause(entry.ctx))
	}
	if len(refs) > 0 {
		bounded, stop := context.WithTimeout(callCtx, lease.SessionExpiresAt.Sub(p.opts.Now()))
		defer stop()
		callCtx = bounded
	}
	discover, stop := context.WithTimeout(callCtx, startupTimeout(p.opts.ConnectTimeout, c))
	items, e := entry.session.Tools(discover)
	stop()
	if e != nil {
		p.retire(canonical, entry)
		return Response{Error: poolError(e, context.Cause(callCtx), id, false)}
	}
	if req.Method == "tools" {
		current, err := p.opts.Load(p.opts.Paths)
		if err != nil {
			return fail(err)
		}
		_, latest, err := current.RuntimeConnection(canonical)
		if err != nil {
			return fail(err)
		}
		if !sameSchemaContext(c, latest, snapshot.Local.CredentialProfiles[c.CredentialProfile], current.Local.CredentialProfiles[latest.CredentialProfile]) {
			return fail(output.NewError("config_changed", nil))
		}
		filtered := []json.RawMessage{}
		for _, raw := range items {
			var tool struct {
				Name string `json:"name"`
			}
			if err = json.Unmarshal(raw, &tool); err != nil {
				return fail(output.NewError("protocol_error", nil))
			}
			if config.ToolAllowed(*latest.ToolPolicy, tool.Name) {
				filtered = append(filtered, raw)
			}
		}
		revisions := map[string]string{"personal": current.Hash}
		if current.Effective != nil {
			revisions = current.Effective.SourceRevisions
		}
		b, err := json.Marshal(output.ToolList{Connection: canonical, Items: filtered, SourceRevisions: revisions})
		if err != nil {
			return fail(err)
		}
		return Response{Data: b}
	}

	var schema json.RawMessage
	found := false
	for _, raw := range items {
		var tool struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"inputSchema"`
		}
		if e = json.Unmarshal(raw, &tool); e != nil {
			return fail(output.NewError("protocol_error", nil))
		}
		if tool.Name == req.Tool {
			schema = tool.InputSchema
			found = true
			break
		}
	}
	if !found {
		return fail(output.NewError("tool_not_found", nil))
	}
	values, checked, e := args.Prepare(req.Arguments, schema)
	if e != nil {
		r := fail(e)
		if errors.Is(e, args.ErrInvalidArgs) {
			r.Error.Message = e.Error()
		}
		return r
	}
	var meta map[string]any
	if req.Meta != nil {
		if meta, e = args.ParseMeta(req.Meta); e != nil {
			return fail(e)
		}
	}
	result, e := entry.session.Call(p.forwardPrompts(callCtx, deadline), req.Tool, values, meta, func() error {
		current, e := p.opts.Load(p.opts.Paths)
		if e != nil {
			return poolError(e, nil, id, false)
		}
		if e = current.CheckTool(canonical, req.Tool); e != nil {
			return poolError(e, nil, id, false)
		}
		_, latest, e := current.RuntimeConnection(canonical)
		if e != nil {
			return poolError(e, nil, id, false)
		}
		if latest.Label != c.Label {
			return output.NewError("config_changed", nil)
		}
		newHash, e := current.ConnectionHash(canonical)
		if e != nil {
			return poolError(e, nil, id, false)
		}
		if newHash != hash {
			return output.NewError("config_changed", nil)
		}
		if callCtx.Err() != nil {
			return poolError(callCtx.Err(), context.Cause(callCtx), id, false)
		}
		if len(refs) > 0 && !p.opts.Now().Before(lease.SessionExpiresAt) {
			return output.NewError("auth_expired", nil)
		}
		p.mu.Lock()
		closed := p.closed
		p.mu.Unlock()
		if closed {
			return output.NewError("canceled", nil)
		}
		if before != nil {
			return before()
		}
		return nil
	})
	resp.Dispatched = result.Dispatched
	var warnings []output.Error
	if !checked {
		warnings = append(warnings, *output.NewError("schema_unchecked", nil))
	}
	if result.Declined != "" {
		if result.DeclineReason == "unavailable" || result.DeclineReason == "unsupported" {
			p.opts.Log("elicitation_declined")
		}
		warnings = append(warnings, *output.ElicitationDeclined(result.DeclineReason, result.Declined))
	}
	var adapterError *output.Error
	certain := errors.As(e, &adapterError) && adapterError != nil && (adapterError.Code == "input_required" || adapterError.Code == "tool_error" || adapterError.Code == "elicitation_declined" || adapterError.Code == "server_error")
	if result.Retire || result.Dispatched && !certain && (e != nil || callCtx.Err() != nil) {
		p.retire(canonical, entry)
	}
	if result.Dispatched && (errors.Is(context.Cause(workCtx), errForced) || errors.Is(context.Cause(callCtx), auth.ErrExpired)) {
		e = output.NewError("outcome_unknown", nil)
	}
	if e == nil && result.IsError {
		e = output.NewError("tool_error", nil)
	}
	if result.NeedsInput {
		e = output.NewError("input_required", nil)
	}
	if e != nil {
		resp.Error = poolError(e, context.Cause(callCtx), id, result.Dispatched)
	}
	if result.Dispatched && (resp.Error == nil || resp.Error.Code == "tool_error" || resp.Error.Code == "input_required") {
		if resp.Data, e = callData(canonical, req.Tool, result, warnings); e != nil {
			resp.Data, resp.Error = nil, poolError(e, nil, id, true)
		}
	}
	return resp
}

func sameSchemaContext(before, after config.Connection, oldProfile, newProfile config.Profile) bool {
	idle := func(c config.Connection) string {
		if c.Lifecycle == nil || c.Lifecycle.IdleTimeout == "" {
			return "session"
		}
		return c.Lifecycle.IdleTimeout
	}
	callTimeout := func(c config.Connection) string {
		if c.CallTimeout == "" {
			return "120s"
		}
		return c.CallTimeout
	}
	return reflect.DeepEqual(before.Transport, after.Transport) &&
		reflect.DeepEqual(before.Auth, after.Auth) &&
		before.CredentialProfile == after.CredentialProfile &&
		(before.CredentialProfile == "" || reflect.DeepEqual(oldProfile, newProfile)) &&
		idle(before) == idle(after) && callTimeout(before) == callTimeout(after)
}

func (p *pool) Shutdown(ctx context.Context, force bool) error {
	p.mu.Lock()
	if !p.closed && !force && len(p.requests) > 0 {
		p.mu.Unlock()
		return output.NewError("runtime_busy", nil)
	}
	p.closed = true
	for _, cancel := range p.requests {
		cancel(errForced)
	}
	p.mu.Unlock()
	p.stopOnce.Do(func() {
		go func() {
			defer close(p.stopDone)
			budget, cancel := context.WithTimeout(context.Background(), p.opts.ShutdownTimeout)
			defer cancel()
			p.mu.Lock()
			entries := p.entries
			p.entries = map[string]*poolEntry{}
			p.mu.Unlock()
			var closing sync.WaitGroup
			for _, entry := range entries {
				closing.Go(func() { entry.close(p, budget) })
			}
			closing.Wait()
			p.workers.Wait()
			p.expiry.Wait()
			if p.opts.Credentials != nil {
				if e := p.opts.Credentials.Close(); e != nil {
					p.stopErr = poolError(e, nil, "", false)
				}
			}
		}()
	})
	select {
	case <-p.stopDone:
		return p.stopErr
	case <-ctx.Done():
		<-p.stopDone
		return p.stopErr
	}
}
