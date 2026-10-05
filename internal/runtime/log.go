package runtime

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/dedene/mcparcel/internal/config"
)

const maxLogBytes = 1 << 20

type boundedLog struct {
	mu   sync.Mutex
	file *os.File
}

func OpenLog(p config.Paths) (io.WriteCloser, error) {
	dir, e := config.OpenPrivateDir(p.StateDir, true)
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
	case "daemon_started", "login_env_fallback", "connection_opened", "connection_closed", "auth_failed", "daemon_stopped",
		"oauth_signed_in", "oauth_refreshed", "oauth_refresh_failed", "oauth_signed_out", "elicitation_declined":
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
