// Package e2e holds end-to-end tests that drive the built lp10 binary against
// the fake transport. Port of the argv-contract and pty-smoke tests, plus
// exit-code coverage for the signal/interrupt paths.
package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/lucasdaddiego/lp10/internal/testutil"
	"github.com/lucasdaddiego/lp10/internal/transport"
)

// coverEnv passes GOCOVERDIR through to a subprocess when LP10_COVERDIR is set, so
// a coverage-instrumented helper binary (built by testutil under the same flag)
// writes its execution coverage there for the merged integration profile. Empty
// (no-op) under a normal `go test` run.
func coverEnv() []string {
	if d := os.Getenv("LP10_COVERDIR"); d != "" {
		return []string{"GOCOVERDIR=" + d}
	}
	return nil
}

func TestArgvContractExits2(t *testing.T) {
	bin := testutil.BuildMain(t)
	cmd := exec.Command(bin, "status")
	cmd.Env = append(os.Environ(), "LP10_ASKPASS=")
	cmd.Env = append(cmd.Env, coverEnv()...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 2 {
		t.Fatalf("exit = %v, want code 2", err)
	}
	if !strings.Contains(errb.String(), "takes no arguments") {
		t.Errorf("stderr = %q", errb.String())
	}
}

// tuiSession boots the lp10 binary in a pty against the fake transport, draws
// for a beat, and returns the running command plus a snapshot of what it drew.
type tuiSession struct {
	cmd  *exec.Cmd
	ptmx *os.File
	mu   *sync.Mutex
	buf  *bytes.Buffer
}

func bootTUI(t *testing.T) *tuiSession { return bootTUISetup(t, nil) }

// bootTUISetup launches the real binary under a pty in a hermetic temp env. If
// setup is non-nil it runs before launch, given the config and state dirs, so a
// test can plant files (e.g. a config.toml) the binary then reads at startup.
func bootTUISetup(t *testing.T, setup func(cfgDir, stateDir string)) *tuiSession {
	t.Helper()
	bin := testutil.BuildMain(t)
	fake := testutil.FakeSSH(t)
	tmp := t.TempDir()
	cfgDir, stateDir := filepath.Join(tmp, "config"), filepath.Join(tmp, "state")
	if setup != nil {
		setup(cfgDir, stateDir)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"LP10_SSH="+fake,
		"LP10_FAKE_SCENARIO=normal",
		"LP10_ASKPASS=",
		"LP10_HOST=",
		"LP10_STATE_DIR="+stateDir,
		"XDG_CONFIG_HOME="+cfgDir,
	)
	cmd.Env = append(cmd.Env, coverEnv()...)
	// Real X/Y pixel dims so the binary's cellPixelSize() reads them via TIOCGWINSZ.
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80, X: 800, Y: 480})
	if err != nil {
		t.Fatalf("pty start: %v", err)
	}
	s := &tuiSession{cmd: cmd, ptmx: ptmx, mu: &sync.Mutex{}, buf: &bytes.Buffer{}}
	go func() {
		b := make([]byte, 4096)
		for {
			n, e := ptmx.Read(b)
			if n > 0 {
				chunk := b[:n]
				s.mu.Lock()
				s.buf.Write(chunk)
				s.mu.Unlock()
				// answer the terminal queries termenv/bubbletea block on
				if bytes.Contains(chunk, []byte("\x1b[6n")) {
					ptmx.Write([]byte("\x1b[1;1R"))
				}
				if bytes.Contains(chunk, []byte("\x1b]11;?")) {
					ptmx.Write([]byte("\x1b]11;rgb:0000/0000/0000\x1b\\"))
				}
			}
			if e != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = cmd.Process.Kill(); ptmx.Close() })
	time.Sleep(2 * time.Second) // let it draw
	return s
}

func (s *tuiSession) output() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// waitExit waits for the process to exit and returns its code (-1 on timeout).
func (s *tuiSession) waitExit(t *testing.T) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case <-done:
		return s.cmd.ProcessState.ExitCode()
	case <-time.After(10 * time.Second):
		s.cmd.Process.Kill()
		t.Fatal("process did not exit")
		return -1
	}
}

func TestTUISmokeUnderPTY(t *testing.T) {
	s := bootTUI(t)
	if !strings.Contains(s.output(), "LP10") {
		t.Error("expected the frame to render the LP10 name")
	}
	s.ptmx.Write([]byte("q"))
	if code := s.waitExit(t); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if out := s.output(); strings.Contains(out, "panic:") || strings.Contains(out, "goroutine ") {
		t.Errorf("crash detected in output:\n%s", out)
	}
}

func TestCtrlCExits130(t *testing.T) {
	s := bootTUI(t)
	s.ptmx.Write([]byte{0x03}) // Ctrl-C
	if code := s.waitExit(t); code != 130 {
		t.Errorf("exit code = %d, want 130 on Ctrl-C", code)
	}
}

func TestSigtermExits143AndCleansUp(t *testing.T) {
	s := bootTUI(t)
	s.cmd.Process.Signal(syscall.SIGTERM)
	code := s.waitExit(t)
	if code != 143 {
		t.Errorf("exit code = %d, want 143 on SIGTERM", code)
	}
	// The terminal-title reset only runs if the signal path performed cleanup
	// (rather than os.Exit-ing from a goroutine) — proves teardown ran.
	if !strings.Contains(s.output(), "\x1b]0;") {
		t.Error("terminal-title reset (cleanup) did not run on SIGTERM")
	}
}

// A SIGINT delivered as a signal (distinct from the Ctrl-C key byte that
// TestCtrlCExits130 sends) is caught by Run's own signal goroutine and maps to
// exit 130 — covering the SIGINT arm that the keyboard path doesn't.
func TestSigintSignalExits130(t *testing.T) {
	s := bootTUI(t)
	s.cmd.Process.Signal(syscall.SIGINT)
	if code := s.waitExit(t); code != 130 {
		t.Errorf("exit code = %d, want 130 on SIGINT", code)
	}
}

// A broken config.toml surfaces as a visible startup warning (config.Load sets
// cfg.Warn; Run threads it to st.Note) rather than being silently ignored —
// covering Run's cfg.Warn branch end to end.
func TestBrokenConfigSurfacesWarning(t *testing.T) {
	s := bootTUISetup(t, func(cfgDir, _ string) {
		dir := filepath.Join(cfgDir, "lp10")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// invalid TOML -> config.Load can't parse it -> cfg.Warn is set
		if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("not = valid = toml ["), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	if out := s.output(); !strings.Contains(out, "config.toml ignored") {
		t.Errorf("a broken config should surface a startup warning; none in output:\n%s", out)
	}
	s.ptmx.Write([]byte("q"))
	s.waitExit(t)
}

// TestAskpassIntegration drives the real binary down its SSH_ASKPASS hot path
// (LP10_ASKPASS=1 → transport.AskpassMain → KeychainPassword → realRunSecurity)
// with a stub secret-store tool on PATH (security on macOS, secret-tool on Linux;
// the per-OS stub scripts live in askpass_{darwin,linux}_test.go), so every store
// outcome is exercised end to end: a returned password, a missing item, a locked
// store, and the lookup tool being absent. This covers the askpass/secret-store
// code the fake transport never reaches (the fake ssh never prompts for a password).
func TestAskpassIntegration(t *testing.T) {
	bin := testutil.BuildMain(t)
	dir := t.TempDir()
	stub := filepath.Join(dir, askpassStubBin)
	writeStub := func(body string) {
		if err := os.WriteFile(stub, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run := func(path string) (out, errOut string, code int) {
		cmd := exec.Command(bin)
		cmd.Env = append([]string{"LP10_ASKPASS=1", "PATH=" + path}, coverEnv()...)
		var o, e bytes.Buffer
		cmd.Stdout, cmd.Stderr = &o, &e
		if ee, ok := cmd.Run().(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return o.String(), e.String(), code
	}

	writeStub("echo hunter2\n") // success: password on stdout, rc 0
	if out, _, code := run(dir); code != 0 || strings.TrimSpace(out) != "hunter2" {
		t.Errorf("success: out=%q code=%d, want hunter2/0", out, code)
	}
	writeStub(askpassNoItemStub) // no item
	if _, errOut, code := run(dir); code != 1 || !strings.Contains(errOut, transport.MarkerNoItem) {
		t.Errorf("no-item: stderr=%q code=%d, want %q/1", errOut, code, transport.MarkerNoItem)
	}
	writeStub(askpassLockedStub) // locked
	if _, errOut, code := run(dir); code != 1 || !strings.Contains(errOut, transport.MarkerLocked) {
		t.Errorf("locked: stderr=%q code=%d, want %q/1", errOut, code, transport.MarkerLocked)
	}
	if _, errOut, code := run(t.TempDir()); code != 1 || !strings.Contains(errOut, transport.MarkerBroken) {
		t.Errorf("broken (no lookup tool on PATH): stderr=%q code=%d, want %q/1", errOut, code, transport.MarkerBroken)
	}
}
