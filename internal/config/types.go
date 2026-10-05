package config

import (
	"errors"
)

var (
	ErrConfig         = errors.New("invalid configuration")
	ErrConfigRequired = errors.New("configuration required")
	ErrUnsafePath     = errors.New("unsafe local path")
	ErrNotFound       = errors.New("connection unavailable")
)

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
}
