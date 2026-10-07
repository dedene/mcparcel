package runtime

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"

	"github.com/dedene/mcparcel/internal/config"
	"github.com/dedene/mcparcel/internal/output"
)

type Status struct {
	Running         bool   `json:"running"`
	PID             int    `json:"pid"`
	ProtocolVersion int    `json:"protocolVersion"`
	BinaryVersion   string `json:"binaryVersion"`
	Compatible      bool   `json:"compatible"`
	Socket          string `json:"socket"`
	Log             string `json:"log"`
	// Executable is the file the daemon runs from: on desktop, its retained
	// copy (<data>/runtime/<version>/mcparcel) unless that copy failed.
	Executable   string     `json:"executable,omitempty"`
	CapturedPath string     `json:"capturedPath"`
	EnvFallback  bool       `json:"envFallback"`
	ActiveCalls  int        `json:"activeCalls"`
	StayAlive    bool       `json:"stayAlive"`
	StartedAt    *time.Time `json:"startedAt"`
	// CredentialSessions lists the daemon's 1Password profile sessions.
	CredentialSessions []CredentialSession `json:"credentialSessions,omitempty"`
}

// CredentialSession is one credential profile session; it never carries an
// account, a reference or a value.
type CredentialSession struct {
	Profile   string    `json:"profile"`
	Mode      string    `json:"mode"`
	State     string    `json:"state"` // active | expired
	ExpiresAt time.Time `json:"expiresAt"`
}

// Probe states.
const (
	ProbeStopped         = "stopped"          // no socket, lock free
	ProbeStaleSocket     = "stale_socket"     // the socket refuses, lock free
	ProbeStarting        = "starting"         // lock held, no answer within the budget
	ProbeRunning         = "running"          // same version, same configuration
	ProbeVersionMismatch = "version_mismatch" // another version answered
	ProbeConfigMismatch  = "config_mismatch"  // same version, another configuration directory
	ProbeUnreadable      = "unreadable"       // connected, but the handshake could not be read
)

// Probe is what a single look at the runtime found.
type Probe struct {
	State         string
	DaemonVersion string // set for running and *_mismatch
	PID           int
	Status        *Status // only when State == ProbeRunning

	// err is the error Status returns for this probe, as before Probe existed:
	// the daemon's own mismatch error, or a failure after the handshake.
	err error
}

// probeBudget bounds Probe and Status.
const probeBudget = 2 * time.Second

func (c *Client) baseStatus() Status {
	return Status{ProtocolVersion: ProtocolVersion, BinaryVersion: c.Version, Compatible: true, Socket: c.Paths.SocketFile, Log: c.Paths.LogFile}
}

// Status reports the runtime without starting it: a stopped runtime (or a
// stale socket) is a not-running status; a mismatched daemon is its error; a
// daemon that does not answer in time is runtime_start_failed.
func (c *Client) Status(ctx context.Context) (Status, error) {
	out := c.baseStatus()
	p, err := c.Probe(ctx)
	if err != nil {
		return out, err
	}
	switch {
	case p.err != nil:
		return out, p.err
	case p.State == ProbeRunning:
		return *p.Status, nil
	case p.State == ProbeStopped || p.State == ProbeStaleSocket:
		return out, nil
	}
	return out, output.NewError("runtime_start_failed", nil)
}

// Probe dials the socket, handshakes with intent "status" keeping the ack (so a
// mismatch still yields the daemon's version and PID), and when compatible
// asks for status. It never starts or waits for a runtime, supervised or not,
// and is bounded by 2 s. Its error is only ErrUnsafePath (runtime dir, socket
// owner/mode, peer UID) or the caller's cancellation.
func (c *Client) Probe(ctx context.Context) (Probe, error) {
	budget, cancel := context.WithTimeout(ctx, probeBudget)
	defer cancel()
	unreadable := false
	for {
		var info handshakeInfo
		r, _, e := c.exchangeAck(budget, "status", Request{Method: "status", Arguments: emptyArgs()}, false, &info)
		if ctx.Err() != nil {
			return Probe{}, callerError(ctx)
		}
		if errors.Is(e, config.ErrUnsafePath) {
			return Probe{}, e
		}
		switch {
		case e == nil:
			status := c.baseStatus()
			if decodeBody(r.Data, &status) != nil {
				return Probe{State: ProbeUnreadable, err: ErrInvalidFrame}, nil
			}
			return Probe{State: ProbeRunning, DaemonVersion: status.BinaryVersion, PID: status.PID, Status: &status}, nil
		case info.connected && info.err == nil:
			// The handshake passed, then the status exchange failed.
			return Probe{State: ProbeUnreadable, DaemonVersion: info.ack.BinaryVersion, PID: info.ack.PID, err: e}, nil
		case info.connected && budget.Err() == nil:
			if p, ok := mismatch(info); ok {
				return p, nil
			}
			var oe *output.Error
			if errors.As(info.err, &oe) {
				// Another frame protocol: refused, but without a readable ack.
				return Probe{State: ProbeUnreadable, err: info.err}, nil
			}
			unreadable = true // timed out or undecodable: retry within the budget
		case !info.connected && budget.Err() == nil:
			held, le := lockHeld(c.Paths)
			if le != nil {
				return Probe{}, le
			}
			if !held && errors.Is(e, syscall.ECONNREFUSED) {
				return Probe{State: ProbeStaleSocket}, nil
			}
			if !held && errors.Is(e, os.ErrNotExist) {
				return Probe{State: ProbeStopped}, nil
			}
		}
		select {
		case <-budget.Done():
		case <-time.After(25 * time.Millisecond):
		}
		if ctx.Err() != nil {
			return Probe{}, callerError(ctx)
		}
		if budget.Err() != nil {
			if unreadable || info.connected {
				return Probe{State: ProbeUnreadable}, nil
			}
			return Probe{State: ProbeStarting}, nil
		}
	}
}

// mismatch reads a refused handshake: the daemon's ack names its version and
// PID. An ack without a version (another frame protocol) is not a mismatch
// this CLI can describe.
func mismatch(info handshakeInfo) (Probe, bool) {
	var oe *output.Error
	if !errors.As(info.err, &oe) || info.ack.BinaryVersion == "" {
		return Probe{}, false
	}
	p := Probe{DaemonVersion: info.ack.BinaryVersion, PID: info.ack.PID, err: info.err}
	switch oe.Code {
	case "runtime_version_mismatch":
		p.State = ProbeVersionMismatch
	case "runtime_config_mismatch":
		p.State = ProbeConfigMismatch
	default:
		return Probe{}, false
	}
	return p, true
}
