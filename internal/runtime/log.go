package runtime

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"sync"

	"github.com/dedene/mcparcel/internal/config"
)

const maxLogBytes = 1 << 20

type boundedLog struct {
	mu   sync.Mutex
	file *os.File
}

func OpenLog(p config.Paths) (io.WriteCloser, error) {
	dir, e := config.OpenPrivateDirUnder(p.StateRoot, p.StateDir, true)
	if e != nil {
		return nil, e
	}
	defer func() { _ = dir.Close() }()
	f, e := config.OpenPrivateFile(dir, "daemon.log", true)
	if e != nil {
		return nil, e
	}
	if _, e = f.Seek(0, io.SeekEnd); e != nil {
		_ = f.Close()
		return nil, config.ErrUnsafePath
	}
	return &boundedLog{file: f}, nil
}

func (w *boundedLog) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(b) > maxLogBytes {
		return 0, io.ErrShortWrite
	}
	st, e := w.file.Stat()
	if e != nil {
		return 0, e
	}
	if st.Size()+int64(len(b)) > maxLogBytes {
		if e = w.file.Truncate(0); e != nil {
			return 0, e
		}
		if _, e = w.file.Seek(0, io.SeekStart); e != nil {
			return 0, e
		}
	}
	return w.file.Write(b)
}
func (w *boundedLog) Close() error { w.mu.Lock(); defer w.mu.Unlock(); return w.file.Close() }
func WriteLog(w io.Writer, event string, path *string) error {
	switch event {
	case "daemon_started", "login_env_fallback", "connection_opened", "connection_closed", "auth_failed", "daemon_stopped", "pid1_no_reaper",
		"oauth_signed_in", "oauth_refreshed", "oauth_refresh_failed", "oauth_signed_out",
		"elicitation_forwarded", "elicitation_accepted", "elicitation_declined", "elicitation_canceled":
	default:
		return errors.New("invalid log event")
	}
	if path != nil && event != "daemon_started" {
		return errors.New("invalid log path")
	}
	b, e := json.Marshal(struct {
		Event string  `json:"event"`
		Path  *string `json:"capturedPath,omitempty"`
	}{event, path})
	if e != nil {
		return e
	}
	b = append(b, '\n')
	n, e := w.Write(b)
	if e == nil && n != len(b) {
		e = io.ErrShortWrite
	}
	return e
}

var (
	signInStages = map[string]bool{"discovery": true, "registration": true, "authorization": true, "callback": true, "token_exchange": true, "token_save": true}
	signInCode   = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)
)

// WriteSignInFailure logs why a sign-in failed: the stage and a sanitized
// error class or OAuth error code. Anything else is refused, so no token,
// code, state value, URL or provider text reaches the log.
func WriteSignInFailure(w io.Writer, stage, code string) error {
	if !signInStages[stage] || !signInCode.MatchString(code) {
		return errors.New("invalid sign-in failure")
	}
	b, e := json.Marshal(struct {
		Event string `json:"event"`
		Stage string `json:"stage"`
		Code  string `json:"code"`
	}{"oauth_sign_in_failed", stage, code})
	if e != nil {
		return e
	}
	b = append(b, '\n')
	n, e := w.Write(b)
	if e == nil && n != len(b) {
		e = io.ErrShortWrite
	}
	return e
}
