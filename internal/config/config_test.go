package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeConfig points XDG_CONFIG_HOME at a temp dir holding the given
// config.toml content (or none, when content == ""), and clears LP10_HOST.
func writeConfig(t *testing.T, content string) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv("LP10_HOST", "")
	if content != "" {
		dir := filepath.Join(base, "lp10")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStateDirHonorsEnv(t *testing.T) {
	d := filepath.Join(t.TempDir(), "s")
	t.Setenv("LP10_STATE_DIR", d)
	got := StateDir()
	if got != d {
		t.Fatalf("StateDir = %q, want %q", got, d)
	}
	if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
		t.Errorf("state dir not created")
	}
}

func TestConfigDefaultsWhenNoFile(t *testing.T) {
	writeConfig(t, "")
	cfg := Load()
	if cfg.Host != "192.168.1.40" || cfg.User != "root" || cfg.VolStep != 2 {
		t.Errorf("defaults wrong: %+v", cfg)
	}
}

func TestConfigFileAndEnvOverride(t *testing.T) {
	writeConfig(t, "host = \"lp10.local\"\nvol_step = 5\n")
	cfg := Load()
	if cfg.Host != "lp10.local" || cfg.VolStep != 5 {
		t.Errorf("file override wrong: %+v", cfg)
	}
	t.Setenv("LP10_HOST", "10.0.0.9")
	if Load().Host != "10.0.0.9" {
		t.Error("LP10_HOST should beat the file")
	}
}

func TestConfigRejectsBoolForInt(t *testing.T) {
	writeConfig(t, "vol_step = true\n")
	if Load().VolStep != 2 {
		t.Error("bool for int field should keep the default")
	}
}

func TestMissingConfigIsSilent(t *testing.T) {
	writeConfig(t, "")
	if Load().Warn != "" {
		t.Error("missing config should not warn")
	}
}

func TestMalformedConfigWarnsAndKeepsDefaults(t *testing.T) {
	writeConfig(t, "host = [broken\n")
	cfg := Load()
	if cfg.Host != "192.168.1.40" {
		t.Errorf("host = %q, want default", cfg.Host)
	}
	if cfg.Warn == "" || !contains(cfg.Warn, "config.toml") {
		t.Errorf("warn = %q, want a config.toml warning", cfg.Warn)
	}
}

func TestNonUTF8ConfigWarnsNotCrashes(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv("LP10_HOST", "")
	dir := filepath.Join(base, "lp10")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte{0xff, 0xfe, 0x00, 'b', 'r', 'o', 'k', 'e', 'n'}, 0o644)
	cfg := Load()
	if cfg.Host != "192.168.1.40" || cfg.Warn == "" {
		t.Errorf("non-utf8 config should warn and keep defaults: %+v", cfg)
	}
}

func TestVolStepClamped(t *testing.T) {
	writeConfig(t, "vol_step = 0\n")
	if Load().VolStep != 1 {
		t.Error("vol_step 0 should clamp to 1")
	}
}

func TestConfigIntFloatCoercion(t *testing.T) {
	writeConfig(t, "vol_step = 2.0\n")
	if Load().VolStep != 2 {
		t.Error("vol_step 2.0 should coerce to 2")
	}
}

func TestConfigHugeFloatVolStepRejected(t *testing.T) {
	writeConfig(t, "vol_step = 1e19\n")
	if Load().VolStep != 2 {
		t.Errorf("out-of-range float vol_step should keep default 2, got %d", Load().VolStep)
	}
}

func TestStateDirFailureDegradesToNoPersistence(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	os.WriteFile(blocker, []byte("not a dir"), 0o644)
	t.Setenv("LP10_STATE_DIR", filepath.Join(blocker, "sub"))
	if StateDir() != "" {
		t.Error("StateDir should be \"\" when it cannot be created")
	}
	cfg := Config{Host: defHost, User: defUser, Name: defName, VolStep: defVolStep}
	if PremutePath(cfg) != "" || SnapshotPath(cfg) != "" {
		t.Error("paths should be empty with no state dir")
	}
	if LoadPremute("") != 30 {
		t.Error("LoadPremute(\"\") should default to 30")
	}
	if LoadSnapshot("") != nil {
		t.Error("LoadSnapshot(\"\") should be nil")
	}
	SavePremute("", 50)   // no-ops, must not panic
	SaveSnapshot("", nil) // no-ops, must not panic
}

func TestSnapshotWithCorruptTrackIsRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "snap.json")
	SaveSnapshot(p, map[string]interface{}{"track": "junk-string", "vol": 4})
	if LoadSnapshot(p) != nil {
		t.Error("snapshot with a string track must be rejected")
	}
}

func TestPremuteRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "premute")
	SavePremute(p, 44)
	if LoadPremute(p) != 44 {
		t.Errorf("premute round-trip = %d, want 44", LoadPremute(p))
	}
}

func TestPremuteDefaultsAndClamps(t *testing.T) {
	if LoadPremute(filepath.Join(t.TempDir(), "missing")) != 30 {
		t.Error("missing premute should default to 30")
	}
	p := filepath.Join(t.TempDir(), "premute")
	SavePremute(p, 250)
	if v := LoadPremute(p); v < 1 || v > 100 {
		t.Errorf("premute = %d, want clamped to [1,100]", v)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "snap.json")
	snap := map[string]interface{}{
		"track": map[string]interface{}{"TrackName": "x"}, "vol": 44, "playing": 0, "pos": 1,
	}
	SaveSnapshot(p, snap)
	got := LoadSnapshot(p)
	if got == nil {
		t.Fatal("snapshot did not round-trip")
	}
	tr, _ := got["track"].(map[string]interface{})
	if tr["TrackName"] != "x" {
		t.Errorf("track.TrackName = %v, want x", tr["TrackName"])
	}
	if LoadSnapshot(filepath.Join(t.TempDir(), "missing.json")) != nil {
		t.Error("missing snapshot should be nil")
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"192.168.1.40":      "192.168.1.40",
		"lp10.local":        "lp10.local",
		"host:with:colons":  "host_with_colons",
		"host with spaces":  "host_with_spaces",
		"Host-With-Dashes":  "Host-With-Dashes",
		"host/with/slashes": "host_with_slashes",
	}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAtomicWriteCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.txt")
	atomicWrite(path, "test content")
	if _, err := os.Stat(path); err != nil {
		t.Error("file should exist after atomicWrite")
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error(".tmp should be gone after success")
	}
	badPath := filepath.Join(t.TempDir(), "nonexistent", "test.txt")
	atomicWrite(badPath, "test") // must not panic
	if _, err := os.Stat(badPath + ".tmp"); err == nil {
		t.Error(".tmp should not linger after a failed write")
	}
}

func TestSavePremuteWithIOErrorIsSwallowed(t *testing.T) {
	badPath := filepath.Join(t.TempDir(), "nonexistent", "premute")
	SavePremute(badPath, 50) // must not panic
	if _, err := os.Stat(badPath); err == nil {
		t.Error("nothing should be written to a bad path")
	}
}

func TestSaveSnapshotWithMarshalErrorIsSwallowed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "snap.json")
	SaveSnapshot(p, make(chan int)) // unmarshalable; must not panic
	if _, err := os.Stat(p); err == nil {
		t.Error("nothing should be written when marshaling fails")
	}
}

func TestLoadSnapshotWithNonDictTrack(t *testing.T) {
	p := filepath.Join(t.TempDir(), "snap.json")
	SaveSnapshot(p, map[string]interface{}{"track": []string{"not", "a", "dict"}, "vol": 44})
	if LoadSnapshot(p) != nil {
		t.Error("non-dict track should reject the snapshot")
	}
}

func TestLoadSnapshotWithNonDictRoot(t *testing.T) {
	p := filepath.Join(t.TempDir(), "snap.json")
	os.WriteFile(p, []byte(`["not","a","dict"]`), 0o644)
	if LoadSnapshot(p) != nil {
		t.Error("non-dict root should reject the snapshot")
	}
}

func TestSavePremuteClampsValue(t *testing.T) {
	p := filepath.Join(t.TempDir(), "premute")
	SavePremute(p, 250)
	if LoadPremute(p) != 100 {
		t.Errorf("250 should clamp to 100, got %d", LoadPremute(p))
	}
	SavePremute(p, -5)
	if LoadPremute(p) != 1 {
		t.Errorf("-5 should clamp to 1, got %d", LoadPremute(p))
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
