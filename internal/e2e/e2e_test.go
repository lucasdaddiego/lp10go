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
	"github.com/lucasdaddiego/lp10go/internal/testutil"
)

func TestArgvContractExits2(t *testing.T) {
	bin := testutil.BuildMain(t)
	cmd := exec.Command(bin, "status")
	cmd.Env = append(os.Environ(), "LP10_ASKPASS=")
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

func bootTUI(t *testing.T) *tuiSession {
	t.Helper()
	bin := testutil.BuildMain(t)
	fake := testutil.FakeSSH(t)
	tmp := t.TempDir()
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"LP10_SSH="+fake,
		"LP10_FAKE_SCENARIO=normal",
		"LP10_ASKPASS=",
		"LP10_HOST=",
		"LP10_STATE_DIR="+filepath.Join(tmp, "state"),
		"XDG_CONFIG_HOME="+filepath.Join(tmp, "config"),
	)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
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
