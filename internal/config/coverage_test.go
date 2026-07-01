package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCov_HomeDir exercises both reachable branches of homeDir: the
// os.UserHomeDir success path (HOME set), and the fallback to user.Current()
// when HOME is empty (on darwin os.UserHomeDir then returns an error).
func TestCov_HomeDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := homeDir(); got != home {
		t.Fatalf("homeDir with HOME=%q = %q, want %q", home, got, home)
	}

	// Empty HOME -> os.UserHomeDir errors -> user.Current() resolves the home.
	t.Setenv("HOME", "")
	if got := homeDir(); got == "" {
		t.Fatal("homeDir fallback returned empty; expected a home via user.Current()")
	}
}

// TestCov_LoadClampsHighVolStep covers the upper clamp branch (vol_step > 100)
// in Load, which the existing suite's lower clamp (0 -> 1) does not reach.
func TestCov_LoadClampsHighVolStep(t *testing.T) {
	writeConfig(t, "vol_step = 200\n")
	if got := Load().VolStep; got != 100 {
		t.Errorf("vol_step 200 should clamp to 100, got %d", got)
	}
}

// TestCov_LoadDerivesBaseFromHome covers the base == "" branch of Load: with
// XDG_CONFIG_HOME unset, the config base is derived from the home dir. Pointing
// HOME at an empty temp dir keeps the real user config out of the picture and
// yields a missing file (defaults, no warning).
func TestCov_LoadDerivesBaseFromHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("LP10_HOST", "")
	cfg := Load()
	if cfg.Host != defHost {
		t.Errorf("Host = %q, want default %q when base is derived from HOME", cfg.Host, defHost)
	}
	if cfg.Warn != "" {
		t.Errorf("no config under the derived base should not warn, got %q", cfg.Warn)
	}
}

// TestCov_ApplyTOMLAllKeys drives every recognized key through applyTOML,
// including the string fields (user, name) the Load-based tests never set, and
// vol_step as an int64.
func TestCov_ApplyTOMLAllKeys(t *testing.T) {
	cfg := Config{
		Host: defHost, User: defUser, Name: DefaultName, VolStep: defVolStep,
		PingHost: defPingHost, Discover: true, Art: true, ArtMode: defArtMode, Mouse: true,
	}
	applyTOML(&cfg, map[string]any{
		"host":      "10.0.0.1",
		"user":      "pi",
		"name":      "Kitchen",
		"ping_host": "example.com",
		"discover":  false,
		"art":       false,
		"art_mode":  "kitty",
		"mouse":     false,
		"vol_step":  int64(7),
	})
	if cfg.Host != "10.0.0.1" || cfg.User != "pi" || cfg.Name != "Kitchen" ||
		cfg.PingHost != "example.com" || cfg.Discover || cfg.Art ||
		cfg.ArtMode != "kitty" || cfg.Mouse || cfg.VolStep != 7 {
		t.Fatalf("applyTOML did not apply every key: %+v", cfg)
	}
}

// TestCov_ApplyTOMLIntegralFloatVolStep covers the float64 case of vol_step:
// an integral float (9.0) within int range is accepted.
func TestCov_ApplyTOMLIntegralFloatVolStep(t *testing.T) {
	cfg := Config{VolStep: defVolStep}
	applyTOML(&cfg, map[string]any{"vol_step": float64(9.0)})
	if cfg.VolStep != 9 {
		t.Errorf("integral float vol_step should apply as 9, got %d", cfg.VolStep)
	}
}

// TestCov_ApplyTOMLRejectsBadValues exercises the rejecting (false) branches:
// an invalid art_mode keeps the default, a non-integral float and an
// out-of-range float are ignored, and a wrong-typed string field is ignored.
func TestCov_ApplyTOMLRejectsBadValues(t *testing.T) {
	cfg := Config{ArtMode: defArtMode, VolStep: defVolStep, Host: defHost}
	applyTOML(&cfg, map[string]any{
		"art_mode": "sixel",      // not in artModes -> ignored
		"vol_step": float64(2.5), // non-integral float -> ignored
		"host":     int64(123),   // wrong type for a string field -> ignored
	})
	if cfg.ArtMode != defArtMode {
		t.Errorf("invalid art_mode should be ignored, got %q", cfg.ArtMode)
	}
	if cfg.VolStep != defVolStep {
		t.Errorf("non-integral float vol_step should be ignored, got %d", cfg.VolStep)
	}
	if cfg.Host != defHost {
		t.Errorf("wrong-typed host should be ignored, got %q", cfg.Host)
	}

	// An integral but out-of-range float must also be rejected (no overflow).
	cfg2 := Config{VolStep: defVolStep}
	applyTOML(&cfg2, map[string]any{"vol_step": 1e19})
	if cfg2.VolStep != defVolStep {
		t.Errorf("out-of-range float vol_step should be ignored, got %d", cfg2.VolStep)
	}
}

// TestCov_StateDirDerivesFromHome covers the LP10_STATE_DIR-unset branch where
// the state dir is built under a present home and created on demand.
func TestCov_StateDirDerivesFromHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LP10_STATE_DIR", "")
	got := StateDir()
	want := filepath.Join(home, ".local", "state", "lp10")
	if got != want {
		t.Fatalf("StateDir = %q, want %q", got, want)
	}
	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Fatalf("state dir not created at %q", got)
	}
}

// TestCov_PremuteAndSnapshotPaths covers the non-empty (StateDir != "") return
// of PremutePath and SnapshotPath.
func TestCov_PremuteAndSnapshotPaths(t *testing.T) {
	d := t.TempDir()
	t.Setenv("LP10_STATE_DIR", d)
	cfg := Config{Host: "lp10.local"}
	if got, want := PremutePath(cfg), filepath.Join(d, "premute-lp10.local"); got != want {
		t.Errorf("PremutePath = %q, want %q", got, want)
	}
	if got, want := SnapshotPath(cfg), filepath.Join(d, "snapshot-lp10.local.json"); got != want {
		t.Errorf("SnapshotPath = %q, want %q", got, want)
	}
}

// TestCov_PathsEmptyWithoutStateDir covers the "" returns of PremutePath,
// SnapshotPath and ArtCacheDir when StateDir cannot be created (its target's
// parent is a regular file).
func TestCov_PathsEmptyWithoutStateDir(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LP10_STATE_DIR", filepath.Join(blocker, "sub"))
	cfg := Config{Host: "h"}
	if got := PremutePath(cfg); got != "" {
		t.Errorf("PremutePath = %q, want empty", got)
	}
	if got := SnapshotPath(cfg); got != "" {
		t.Errorf("SnapshotPath = %q, want empty", got)
	}
	if got := ArtCacheDir(); got != "" {
		t.Errorf("ArtCacheDir = %q, want empty", got)
	}
}

// TestCov_ArtCacheDirMkdirFails covers ArtCacheDir's own MkdirAll-failure
// return: the state dir exists, but a regular file named "art" inside it blocks
// creating the art subdirectory.
func TestCov_ArtCacheDirMkdirFails(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "art"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LP10_STATE_DIR", d)
	if got := ArtCacheDir(); got != "" {
		t.Errorf("ArtCacheDir = %q, want empty when <state>/art is a file", got)
	}
}

// TestCov_LoadPremuteNonNumeric covers the Atoi-failure branch: non-numeric
// file content defaults to 30.
func TestCov_LoadPremuteNonNumeric(t *testing.T) {
	p := filepath.Join(t.TempDir(), "premute")
	if err := os.WriteFile(p, []byte("not-a-number"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LoadPremute(p); got != 30 {
		t.Errorf("non-numeric premute should default to 30, got %d", got)
	}
}

// TestCov_LoadSnapshotInvalidJSON covers the json.Unmarshal-failure branch:
// malformed JSON yields nil.
func TestCov_LoadSnapshotInvalidJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "snap.json")
	if err := os.WriteFile(p, []byte("{ this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LoadSnapshot(p); got != nil {
		t.Errorf("invalid JSON snapshot should be nil, got %v", got)
	}
}

// TestCov_LoadSnapshotTrackVariants covers the accepting paths of the track
// guard: a snapshot with no "track" key (present == false) and one with an
// explicit null track (present, but nil) both round-trip.
func TestCov_LoadSnapshotTrackVariants(t *testing.T) {
	p := filepath.Join(t.TempDir(), "snap.json")

	SaveSnapshot(p, map[string]any{"vol": 12})
	if got := LoadSnapshot(p); got == nil || got["vol"] == nil {
		t.Errorf("snapshot without a track key should round-trip, got %v", got)
	}

	SaveSnapshot(p, map[string]any{"track": nil, "vol": 3})
	if got := LoadSnapshot(p); got == nil {
		t.Error("snapshot with an explicit null track should be accepted")
	}
}

// TestCov_ClampVol covers clampVol directly across its three branches.
func TestCov_ClampVol(t *testing.T) {
	cases := map[int]int{-7: 1, 0: 1, 1: 1, 50: 50, 100: 100, 250: 100}
	for in, want := range cases {
		if got := clampVol(in); got != want {
			t.Errorf("clampVol(%d) = %d, want %d", in, got, want)
		}
	}
}
