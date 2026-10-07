package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/dedene/mcparcel/internal/config"
)

// HealthKind names one OAuth session event.
type HealthKind string

const (
	HealthAuthorized              HealthKind = "authorized"
	HealthRefreshed               HealthKind = "refreshed"
	HealthRefreshFailed           HealthKind = "refresh_failed"
	HealthReauthorizationRequired HealthKind = "reauthorization_required"
	HealthLogout                  HealthKind = "logout"
)

// What started a refresh or sign-in.
const (
	TriggerCall      = "call"
	TriggerKeepAlive = "keep_alive"
	TriggerStart     = "start"
	TriggerLogin     = "login"
)

const (
	healthFile      = "oauth-health.json"
	healthMaxEvents = 50
	// codeUnreachable: a refresh whose token request never reached the provider.
	codeUnreachable = "unreachable"
)

// healthMaxBytes bounds the encoded file; a variable so tests can lower it.
var healthMaxBytes = 1 << 20

// HealthEvent is one recorded authorization, refresh or failure. It never
// holds token material: codes are sanitized, durations are seconds.
type HealthEvent struct {
	At           int64      `json:"at"`
	Kind         HealthKind `json:"kind"`
	Trigger      string     `json:"trigger,omitempty"`
	Code         string     `json:"code,omitempty"`
	HTTPStatus   int        `json:"status,omitempty"`
	Terminal     bool       `json:"terminal,omitempty"`
	AccessTTL    int64      `json:"accessTtl,omitempty"`
	RefreshTTL   int64      `json:"refreshTtl,omitempty"`
	RefreshToken bool       `json:"refreshToken,omitempty"`
	Rotated      bool       `json:"rotated,omitempty"`
	Idle         int64      `json:"idle,omitempty"`
	Interrupted  bool       `json:"interrupted,omitempty"`
	ReusedClient bool       `json:"reusedClient,omitempty"`
}

// ConnectionHealth is one connection's history. Pending is set while a refresh
// may have rotated the refresh token without the new one being saved. Client
// is a hash of the dynamically registered client ID, Redirect the loopback
// redirect it was registered with.
type ConnectionHealth struct {
	Pending  int64         `json:"pending,omitempty"`
	Client   string        `json:"client,omitempty"`
	Redirect string        `json:"redirect,omitempty"`
	Events   []HealthEvent `json:"events"`
}

type healthDoc struct {
	V           int                         `json:"v"`
	Connections map[string]ConnectionHealth `json:"connections"`
}

// Health is the daemon's writer of oauth-health.json. Writes are best effort;
// all methods on a nil *Health do nothing.
type Health struct {
	dir string
	now func() time.Time

	mu        sync.Mutex
	data      map[string]ConnectionHealth
	prior     map[string]bool   // a refresh began while an earlier one was still pending
	successes map[string]uint64 // authorizations and refreshes recorded by this process
	loaded    bool
}

func NewHealth(stateDir string, now func() time.Time) *Health {
	if now == nil {
		now = time.Now
	}
	return &Health{dir: stateDir, now: now, prior: map[string]bool{}, successes: map[string]uint64{}}
}

// ClientHash identifies a client ID without storing it.
func ClientHash(clientID string) string {
	sum := sha256.Sum256([]byte(clientID))
	return hex.EncodeToString(sum[:])[:12]
}

// Record appends e to the account's history and writes the file.
func (h *Health) Record(account string, e HealthEvent) error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.connLocked(account)
	e.At = h.now().Unix()
	if e.Code != "" {
		e.Code = sanitizeCode(e.Code, "error")
	}
	if e.Kind == HealthRefreshed || e.Kind == HealthRefreshFailed {
		if last := lastSuccess(c.Events); last != nil {
			e.Idle = max(0, e.At-last.At)
		}
	}
	if e.Kind == HealthRefreshFailed && e.Code == "invalid_grant" && c.Pending != 0 && h.prior[account] && mayRotate(c.Events) {
		e.Interrupted = true
	}
	switch {
	case e.Kind == HealthRefreshed, e.Kind == HealthAuthorized, e.Kind == HealthLogout, e.Terminal,
		// An HTTP answer or a request that never reached the provider: no
		// new refresh token was issued, so nothing can have been lost.
		e.Kind == HealthRefreshFailed && (e.HTTPStatus != 0 || e.Code == codeUnreachable):
		c.Pending = 0
		delete(h.prior, account)
	}
	if e.Kind == HealthLogout {
		c.Client, c.Redirect = "", ""
	}
	if e.Kind == HealthAuthorized || e.Kind == HealthRefreshed {
		h.successes[account]++
	}
	c.Events = append(c.Events, e)
	if n := len(c.Events) - healthMaxEvents; n > 0 {
		c.Events = append([]HealthEvent(nil), c.Events[n:]...)
	}
	h.data[account] = c
	return h.writeLocked()
}

// BeginRefresh marks a refresh as pending before its token request. A marker
// left by an earlier refresh is kept, and a later invalid_grant is then
// recorded as interrupted.
func (h *Health) BeginRefresh(account string) error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.connLocked(account)
	h.prior[account] = c.Pending != 0
	if c.Pending == 0 {
		c.Pending = h.now().Unix()
	}
	h.data[account] = c
	return h.writeLocked()
}

// RememberClient stores the hash of a registered client and its redirect; an
// empty clientID forgets both.
func (h *Health) RememberClient(account, clientID, redirect string) error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.connLocked(account)
	if clientID == "" {
		c.Client, c.Redirect = "", ""
	} else {
		c.Client, c.Redirect = ClientHash(clientID), redirect
	}
	h.data[account] = c
	return h.writeLocked()
}

// Client returns the remembered client hash and redirect.
func (h *Health) Client(account string) (hash, redirect string) {
	if h == nil {
		return "", ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.connLocked(account)
	return c.Client, c.Redirect
}

// LastSuccess is the time of the last authorization or refresh, zero if none.
func (h *Health) LastSuccess(account string) time.Time {
	if h == nil {
		return time.Time{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if last := lastSuccess(h.connLocked(account).Events); last != nil {
		return time.Unix(last.At, 0)
	}
	return time.Time{}
}

// mayRotate reports whether the provider may rotate refresh tokens: unknown
// until a refresh is recorded, then whatever the last one did.
func mayRotate(events []HealthEvent) bool {
	r := lastKind(events, HealthRefreshed)
	return r == nil || r.Rotated
}

// successCount counts the authorizations and refreshes this process recorded
// for account. Unlike their times it only grows, whatever the wall clock does.
func (h *Health) successCount(account string) uint64 {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.successes[account]
}

func lastSuccess(events []HealthEvent) *HealthEvent {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == HealthAuthorized || events[i].Kind == HealthRefreshed {
			return &events[i]
		}
	}
	return nil
}

func (h *Health) connLocked(account string) ConnectionHealth {
	if !h.loaded {
		h.data, _ = ReadHealth(h.dir)
		h.loaded = true
	}
	return h.data[account]
}

// ReadHealth reads the file. A missing directory or file is an empty map; an
// unsafe, oversized, corrupt or unknown-version file is an empty map and an
// error.
func ReadHealth(stateDir string) (map[string]ConnectionHealth, error) {
	empty := map[string]ConnectionHealth{}
	dir, err := config.OpenPrivateDir(stateDir, false)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	defer dir.Close()
	f, err := config.OpenPrivateFile(dir, healthFile, false)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(healthMaxBytes)+1))
	if err != nil {
		return empty, err
	}
	if len(b) > healthMaxBytes {
		return empty, errors.New("health file too large")
	}
	var doc healthDoc
	if err := json.Unmarshal(b, &doc); err != nil || doc.V != 1 {
		return empty, errors.New("invalid health file")
	}
	if doc.Connections == nil {
		return empty, nil
	}
	return doc.Connections, nil
}

// writeLocked replaces the file atomically, evicting the connections with the
// oldest last event while it exceeds healthMaxBytes.
func (h *Health) writeLocked() error {
	var b []byte
	for {
		var err error
		if b, err = json.Marshal(healthDoc{V: 1, Connections: h.data}); err != nil {
			return err
		}
		if len(b) <= healthMaxBytes || len(h.data) == 0 {
			break
		}
		delete(h.data, oldestConnection(h.data))
	}
	return replaceStateFile(h.dir, healthFile, b)
}

func oldestConnection(data map[string]ConnectionHealth) string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	last := func(c ConnectionHealth) int64 {
		if len(c.Events) == 0 {
			return 0
		}
		return c.Events[len(c.Events)-1].At
	}
	oldest := keys[0]
	for _, k := range keys[1:] {
		if last(data[k]) < last(data[oldest]) {
			oldest = k
		}
	}
	return oldest
}

// replaceStateFile atomically replaces the private file name in stateDir.
func replaceStateFile(stateDir, name string, data []byte) (err error) {
	dir, err := config.OpenPrivateDir(stateDir, true)
	if err != nil {
		return err
	}
	defer dir.Close()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := "." + strings.TrimSuffix(name, ".json") + "-" + hex.EncodeToString(nonce[:]) + ".tmp"
	dirfd := int(dir.Fd())
	fd, err := unix.Openat(dirfd, temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), temp)
	renamed := false
	defer func() {
		if !renamed {
			_ = file.Close()
			_ = unix.Unlinkat(dirfd, temp, 0)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := unix.Renameat(dirfd, temp, dirfd, name); err != nil {
		return err
	}
	renamed = true
	_ = file.Close()
	return dir.Sync()
}
