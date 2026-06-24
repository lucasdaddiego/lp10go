// Package config handles the config file, paths, and persistent-state IO
// (premute level, snapshot cache, atomic writes). Port of lp10lib/config.py.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// homeDir resolves the user's home directory, falling back to the passwd
// database (like Python's os.path.expanduser) when $HOME is unset, rather than
// silently producing a cwd-relative path.
func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return u.HomeDir
	}
	return ""
}

// Defaults mirror config.DEFAULTS. Field order is irrelevant; types drive the
// strict TOML coercion below.
const (
	defHost     = "lp10.local" // fallback host; discovery (on by default) finds the real one
	defUser     = "root"
	defName     = "LP10 · Living"
	defVolStep  = 2
	defPingHost = "spotify.com" // diagnostics: the device's internet-latency target
)

// HostEnv pins the device host for a single run, overriding config and skipping
// mDNS discovery.
const HostEnv = "LP10_HOST"

// Config is the resolved runtime configuration. Warn carries a config-load
// problem to surface in the UI (empty string == no warning).
type Config struct {
	Host       string
	User       string
	Name       string
	VolStep    int
	PingHost   string // diagnostics overlay: device's internet-ping target
	Discover   bool   // attempt mDNS auto-discovery at startup (config input)
	Discovered bool   // set at runtime when discovery resolved the host
	Warn       string
}

// Load reads ~/.config/lp10/config.toml (honoring XDG_CONFIG_HOME), applies the
// same strict per-field typing as the Python version, clamps vol_step, and lets
// LP10_HOST override the host for a single run.
func Load() Config {
	cfg := Config{Host: defHost, User: defUser, Name: defName, VolStep: defVolStep, PingHost: defPingHost, Discover: true}

	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		if h := homeDir(); h != "" {
			base = filepath.Join(h, ".config")
		}
	}
	// Only read a config file when we have a real base dir: a "" home must not
	// resolve to a cwd-relative <cwd>/.config path that varies run to run.
	if base != "" {
		path := filepath.Join(base, "lp10", "config.toml")
		var data map[string]interface{}
		_, err := toml.DecodeFile(path, &data)
		switch {
		case err == nil:
			applyTOML(&cfg, data)
		case errors.Is(err, fs.ErrNotExist):
			// missing file is identical to no config — no warning
		default:
			// a broken file must not be silently identical to a missing one
			cfg.Warn = fmt.Sprintf("config.toml ignored: %v", err)
		}
	}

	// Clamp to a sane [1,100]: 0/negative would freeze the volume keys, and an
	// absurd step (e.g. a hostile config, or a float that saturated to MaxInt)
	// would overflow AdjustVol before clamp100 rescued it.
	if cfg.VolStep < 1 {
		cfg.VolStep = 1
	} else if cfg.VolStep > 100 {
		cfg.VolStep = 100
	}
	if h := os.Getenv(HostEnv); h != "" {
		cfg.Host = h
	}
	return cfg
}

// applyTOML copies recognized keys with strict typing: string fields accept
// only strings; vol_step accepts an integer or an integral float. Anything else
// (including a bool, or a string for a numeric field) is silently ignored, to
// avoid surprising coercions from typos.
func applyTOML(cfg *Config, data map[string]interface{}) {
	if v, ok := data["host"].(string); ok {
		cfg.Host = v
	}
	if v, ok := data["user"].(string); ok {
		cfg.User = v
	}
	if v, ok := data["name"].(string); ok {
		cfg.Name = v
	}
	if v, ok := data["ping_host"].(string); ok {
		cfg.PingHost = v
	}
	if v, ok := data["discover"].(bool); ok {
		cfg.Discover = v
	}
	switch v := data["vol_step"].(type) {
	case int64:
		cfg.VolStep = int(v)
	case float64:
		// allow an integral float like 2.0, but reject values outside int range
		// so the conversion can't overflow to a garbage/negative step
		if v == math.Trunc(v) && v >= float64(math.MinInt) && v <= float64(math.MaxInt) {
			cfg.VolStep = int(v)
		}
	}
}

// StateDir is the persistent-state directory, or "" when it cannot be created —
// callers degrade to a session without persistence rather than crashing.
func StateDir() string {
	d := os.Getenv("LP10_STATE_DIR")
	if d == "" {
		h := homeDir()
		if h == "" {
			return "" // no home: degrade to no-persistence, not a cwd-relative dir
		}
		d = filepath.Join(h, ".local", "state", "lp10")
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return ""
	}
	return d
}

var slugRe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func slug(host string) string {
	return slugRe.ReplaceAllString(host, "_")
}

// PremutePath / SnapshotPath are per-host files under the state dir, or "" when
// there is no usable state dir.
func PremutePath(cfg Config) string {
	if d := StateDir(); d != "" {
		return filepath.Join(d, "premute-"+slug(cfg.Host))
	}
	return ""
}

func SnapshotPath(cfg Config) string {
	if d := StateDir(); d != "" {
		return filepath.Join(d, "snapshot-"+slug(cfg.Host)+".json")
	}
	return ""
}

// atomicWrite writes via a deterministic .tmp sibling then renames: a writer
// frozen mid-write leaves exactly one well-known file, overwritten next run.
func atomicWrite(path, text string) {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		os.Remove(tmp)
		return
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
	}
}

func clampVol(v int) int {
	if v < 1 {
		return 1
	}
	if v > 100 {
		return 100
	}
	return v
}

// LoadPremute returns the persisted pre-mute level clamped to [1,100], or 30 on
// any problem (missing path, unreadable, or non-numeric content).
func LoadPremute(path string) int {
	if path == "" {
		return 30
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return 30
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 30
	}
	return clampVol(n)
}

// SavePremute persists a clamped pre-mute level. Failures are swallowed.
func SavePremute(path string, v int) {
	if path == "" {
		return
	}
	atomicWrite(path, strconv.Itoa(clampVol(v)))
}

// LoadSnapshot reads the cached snapshot. A corrupt file (not an object, or a
// non-object/non-null "track") returns nil so it cannot become a crash loop.
func LoadSnapshot(path string) map[string]interface{} {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var v interface{}
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	snap, ok := v.(map[string]interface{})
	if !ok {
		return nil
	}
	if tr, present := snap["track"]; present && tr != nil {
		if _, ok := tr.(map[string]interface{}); !ok {
			return nil
		}
	}
	return snap
}

// SaveSnapshot persists the snapshot as JSON. Failures are swallowed.
func SaveSnapshot(path string, snap interface{}) {
	if path == "" {
		return
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return
	}
	atomicWrite(path, string(b))
}
