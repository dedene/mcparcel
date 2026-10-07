package config

import (
	"errors"
)

var (
	ErrConfig         = errors.New("invalid configuration")
	ErrConfigRequired = errors.New("configuration required")
	ErrUnsafePath     = errors.New("unsafe local path")
	ErrNotFound       = errors.New("connection unavailable")
	// ErrNotCreated marks an ErrUnsafePath from a private directory or file
	// that could not be created (a read-only, full or unwritable parent), not
	// one that failed a safety check. Only a caller that can do without the
	// directory tells the two apart; to everyone else it is ErrUnsafePath.
	ErrNotCreated = errors.New("directory could not be created")
)

type notCreatedError struct{}

func (notCreatedError) Error() string { return ErrUnsafePath.Error() }
func (notCreatedError) Is(target error) bool {
	return target == ErrUnsafePath || target == ErrNotCreated
}

type InputRef struct {
	Input string `json:"input"`
}
type Value struct {
	Literal *string    `json:"-"`
	Input   *InputRef  `json:"-"`
	Secret  *SecretRef `json:"-"`
}
type SecretRef struct {
	Secret string `json:"secret"`
	Prefix string `json:"prefix,omitempty"`
	Suffix string `json:"suffix,omitempty"`
}
type Stdio struct {
	Command    Value            `json:"command"`
	Args       []Value          `json:"args,omitempty"`
	Env        map[string]Value `json:"env,omitempty"`
	Cwd        *Value           `json:"cwd,omitempty"`
	InheritEnv []string         `json:"inheritEnv,omitempty"`
}
type HTTP struct {
	URL               Value            `json:"url"`
	Headers           map[string]Value `json:"headers,omitempty"`
	Mode              string           `json:"mode,omitempty"`
	AllowInsecureHTTP string           `json:"allowInsecureHttp,omitempty"`
}
type Transport struct {
	Stdio *Stdio `json:"-"`
	HTTP  *HTTP  `json:"-"`
}
type Snapshot struct {
	Personal  Personal         `json:"-"`
	Local     Local            `json:"-"`
	Hash      string           `json:"-"`
	Effective *EffectiveConfig `json:"-"`
	Revision  uint64           `json:"-"`
}
type Paths struct {
	Home           string `json:"-"`
	ConfigDir      string `json:"-"`
	DataDir        string `json:"-"`
	CacheDir       string `json:"-"`
	StateDir       string `json:"-"`
	RuntimeDir     string `json:"-"`
	PersonalFile   string `json:"-"`
	ConfigFile     string `json:"-"`
	SelectionsFile string `json:"-"`
	SocketFile     string `json:"-"`
	LockFile       string `json:"-"`
	LogFile        string `json:"-"`
	// StateRoot is the headless state root the four directories above derive
	// from; "" outside headless mode.
	StateRoot string `json:"-"`
	// Supervised is config.json's runtime.supervised: a supervisor owns the
	// runtime whether or not runtime serve has marked the state root yet.
	Supervised bool `json:"-"`
}
