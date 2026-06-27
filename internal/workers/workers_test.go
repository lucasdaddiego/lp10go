package workers

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/testutil"
	"github.com/lucasdaddiego/lp10/internal/transport"
)

func waitFor(pred func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return pred()
}

type startOpts struct {
	fastFatal bool
	watchdog  *struct{ silent, connect, dataless time.Duration }
	ssh       string
}

type harness struct {
	t       *testing.T
	st      *protocol.State
	fakeSSH string
	tmp     string
}

func newHarness(t *testing.T) *harness {
	testutil.Isolate(t)
	tmp := t.TempDir()
	t.Setenv("LP10_FAKE_DIR", tmp)
	h := &harness{t: t, st: protocol.NewState(), fakeSSH: testutil.FakeSSH(t), tmp: tmp}
	t.Cleanup(func() {
		h.st.Stop.Set()
		if proc := h.st.Sproc(); proc != nil {
			if proc.Cmd.Process != nil {
				proc.Cmd.Process.Kill()
			}
			proc.WaitTimeout(3 * time.Second)
		}
	})
	return h
}

func (h *harness) start(scenario string, opts startOpts) *protocol.State {
	ssh := h.fakeSSH
	if opts.ssh != "" {
		ssh = opts.ssh
	}
	h.t.Setenv("LP10_SSH", ssh)
	h.t.Setenv("LP10_FAKE_SCENARIO", scenario)
	if opts.fastFatal {
		orig := classify
		classify = func(s string) *transport.TransportError {
			if e := orig(s); e != nil {
				e.Cadence = 200 * time.Millisecond
				return e
			}
			return nil
		}
		h.t.Cleanup(func() { classify = orig })
	}
	cfg := config.Load()
	go StreamWorker(h.st, cfg)
	if opts.watchdog != nil {
		w := opts.watchdog
		if w.dataless == 0 {
			w.dataless = DatalessAfter
		}
		go Watchdog(h.st, w.silent, w.connect, w.dataless)
	}
	return h.st
}

// ---- integration: stream/command/watchdog against the fake transport --------

func TestNormalStreamConnectsAndParses(t *testing.T) {
	h := newHarness(t)
	st := h.start("normal", startOpts{})
	if !waitFor(func() bool { return st.Snap().Connected }, 6*time.Second) {
		t.Fatal("never connected")
	}
	s := st.Snap()
	if s.Track.Str("TrackName") != "De Música Ligera" || s.Vol != 44 || s.Playing != 0 {
		t.Errorf("snap = %+v", s)
	}
}

func TestGarbageStreamStillParses(t *testing.T) {
	h := newHarness(t)
	st := h.start("garbage", startOpts{})
	if !waitFor(func() bool { return st.Snap().Track != nil }, 6*time.Second) {
		t.Fatal("track never parsed")
	}
	// raw pos 31000 only arrives via post-noise heartbeats, proving the parser
	// survives mid-stream garbage (not just the leading record's 30000).
	if !waitFor(func() bool { return st.RawPos() >= 31000 }, 6*time.Second) {
		t.Fatal("post-garbage heartbeat never parsed")
	}
}

func TestEofReconnects(t *testing.T) {
	h := newHarness(t)
	st := h.start("eof", startOpts{})
	if !waitFor(func() bool { return st.RawAttempts() >= 3 }, 6*time.Second) {
		t.Fatalf("attempts = %d, want >= 3", st.RawAttempts())
	}
}

func TestSilentStreamTripsWatchdogAndRecycles(t *testing.T) {
	h := newHarness(t)
	st := h.start("silent", startOpts{watchdog: &struct{ silent, connect, dataless time.Duration }{
		silent: 300 * time.Millisecond, connect: 2 * time.Second}})
	if !waitFor(func() bool { return st.RawAttempts() >= 2 }, 8*time.Second) {
		t.Fatalf("attempts = %d, want >= 2", st.RawAttempts())
	}
}

func TestDatalessFromBirthRecycles(t *testing.T) {
	h := newHarness(t)
	st := h.start("dataless", startOpts{watchdog: &struct{ silent, connect, dataless time.Duration }{
		silent: 10 * time.Second, connect: 10 * time.Second, dataless: 400 * time.Millisecond}})
	if !waitFor(func() bool { return st.RawAttempts() >= 2 }, 8*time.Second) {
		t.Fatalf("attempts = %d, want >= 2", st.RawAttempts())
	}
}

func TestSnapshotPersistsDuringStream(t *testing.T) {
	h := newHarness(t)
	h.st.SnapshotFile = filepath.Join(h.tmp, "snap.json")
	st := h.start("normal", startOpts{})
	// The device sends its one-shot @@i block before the first PlayView, so the
	// very first persisted snapshot can be track-less; wait for the track itself.
	if !waitFor(func() bool {
		snap := config.LoadSnapshot(st.SnapshotFile)
		if snap == nil {
			return false
		}
		tr, _ := snap["track"].(map[string]interface{})
		return tr["TrackName"] == "De Música Ligera"
	}, 8*time.Second) {
		t.Fatalf("track snapshot never persisted: %v", config.LoadSnapshot(st.SnapshotFile))
	}
}

func TestSpawnFailureIsNotedAndRetried(t *testing.T) {
	h := newHarness(t)
	st := h.start("normal", startOpts{ssh: filepath.Join(h.tmp, "missing-ssh")})
	if !waitFor(func() bool { return strings.Contains(st.Snap().Error, "cannot start ssh") }, 6*time.Second) {
		t.Fatalf("error = %q, want 'cannot start ssh'", st.Snap().Error)
	}
}

func TestUnclassifiedSSHStderrIsSurfaced(t *testing.T) {
	h := newHarness(t)
	fail := filepath.Join(h.tmp, "failing-ssh")
	os.WriteFile(fail, []byte("#!/bin/sh\necho 'ssh: Could not resolve hostname nope' >&2\nexit 255\n"), 0o755)
	st := h.start("normal", startOpts{ssh: fail})
	if !waitFor(func() bool { return strings.Contains(st.Snap().Error, "Could not resolve hostname") }, 6*time.Second) {
		t.Fatalf("error = %q", st.Snap().Error)
	}
	if st.Snap().Fatal {
		t.Error("a transient stderr must not be fatal")
	}
}

func TestAuthfailIsFatalWithRemediation(t *testing.T) {
	h := newHarness(t)
	st := h.start("authfail", startOpts{fastFatal: true})
	if !waitFor(func() bool { return st.Snap().Fatal }, 6*time.Second) {
		t.Fatal("never went fatal")
	}
	e := st.Snap().Error
	if !strings.Contains(e, "password rejected") || !strings.Contains(e, transport.StoreHint) {
		t.Errorf("error = %q", e)
	}
}

func TestKeychainLockedIsDistinct(t *testing.T) {
	h := newHarness(t)
	st := h.start("keychain-locked", startOpts{fastFatal: true})
	if !waitFor(func() bool { return st.Snap().Fatal }, 6*time.Second) {
		t.Fatal("never went fatal")
	}
	e := st.Snap().Error
	if !strings.Contains(e, "is locked") || strings.Contains(e, "password rejected") {
		t.Errorf("error = %q", e)
	}
}

func TestHealClearsFatalError(t *testing.T) {
	h := newHarness(t)
	t.Setenv("LP10_FAKE_HEAL_AFTER", "1")
	st := h.start("heal", startOpts{fastFatal: true})
	if !waitFor(func() bool { return st.Snap().Fatal }, 6*time.Second) {
		t.Fatal("first attempt should be fatal")
	}
	if !waitFor(func() bool { return st.Snap().Connected }, 15*time.Second) {
		t.Fatal("never healed to connected")
	}
	s := st.Snap()
	if s.Fatal || s.Error != "" {
		t.Errorf("after heal: fatal=%v error=%q", s.Fatal, s.Error)
	}
}

func TestCommandsReachDeviceAndTeardownIsClean(t *testing.T) {
	h := newHarness(t)
	log := filepath.Join(h.tmp, "cmdlog")
	t.Setenv("LP10_FAKE_CMDLOG", log)
	st := h.start("normal", startOpts{})
	cmds := make(chan *protocol.Command, 64)
	go CommandWorker(st, cmds, CommandDeadline)
	if !waitFor(func() bool { return st.Snap().Connected }, 6*time.Second) {
		t.Fatal("never connected")
	}
	cmds <- &protocol.Command{Mid: 40, Data: "NEXT", TS: time.Now()}
	cmds <- &protocol.Command{Mid: 64, Data: "30", TS: time.Now()}
	if !waitFor(func() bool { return logContains(log, "40 NEXT") && logContains(log, "64 30") }, 6*time.Second) {
		t.Fatalf("cmdlog = %q", readFile(log))
	}
	proc := st.Sproc()
	Teardown(st, cmds, DrainTimeout)
	if proc != nil && !proc.WaitTimeout(3*time.Second) {
		t.Error("child not reaped after teardown")
	}
}

func TestFailedSendsDeliverInOrderAfterReconnect(t *testing.T) {
	h := newHarness(t)
	log := filepath.Join(h.tmp, "ordlog")
	t.Setenv("LP10_FAKE_CMDLOG", log)
	cmds := make(chan *protocol.Command, 64)
	go CommandWorker(h.st, cmds, 15*time.Second)
	now := time.Now()
	cmds <- &protocol.Command{Mid: 40, Data: "NEXT", TS: now}
	cmds <- &protocol.Command{Mid: 40, Data: "PREV", TS: now}
	time.Sleep(500 * time.Millisecond)
	h.start("normal", startOpts{})
	if !waitFor(func() bool { return logContains(log, "40 NEXT") && logContains(log, "40 PREV") }, 8*time.Second) {
		t.Fatalf("cmdlog = %q", readFile(log))
	}
	txt := readFile(log)
	if strings.Index(txt, "40 NEXT") >= strings.Index(txt, "40 PREV") {
		t.Errorf("order not preserved: %q", txt)
	}
}

func TestStaleCommandsDropVisibly(t *testing.T) {
	h := newHarness(t) // no stream started: command stays queued and ages out
	cmds := make(chan *protocol.Command, 64)
	go CommandWorker(h.st, cmds, 200*time.Millisecond)
	cmds <- &protocol.Command{Mid: 40, Data: "NEXT", TS: time.Now().Add(-time.Second)}
	if !waitFor(func() bool { return h.st.Snap().Error == "command not delivered" }, 6*time.Second) {
		t.Fatalf("error = %q", h.st.Snap().Error)
	}
}

func TestFreshCommandNotAgedByOlderPendingOne(t *testing.T) {
	h := newHarness(t)
	log := filepath.Join(h.tmp, "agelog")
	t.Setenv("LP10_FAKE_CMDLOG", log)
	cmds := make(chan *protocol.Command, 64)
	go CommandWorker(h.st, cmds, 4*time.Second)
	now := time.Now()
	cmds <- &protocol.Command{Mid: 64, Data: "10", TS: now.Add(-3700 * time.Millisecond)} // old but not stale
	cmds <- &protocol.Command{Mid: 40, Data: "NEXT", TS: now}                             // fresh
	time.Sleep(1 * time.Second)                                                           // old one expires while pending
	h.start("normal", startOpts{})
	if !waitFor(func() bool { return logContains(log, "40 NEXT") }, 8*time.Second) {
		t.Fatalf("fresh command never delivered; cmdlog = %q", readFile(log))
	}
}

// ---- targeted unit tests ----------------------------------------------------

func TestSelfSnapReturnsSubsetOfState(t *testing.T) {
	snap := selfSnap(protocol.NewState())
	want := map[string]bool{"track": true, "pos": true, "playing": true, "vol": true, "eq": true}
	if len(snap) != len(want) {
		t.Fatalf("keys = %v", snap)
	}
	for k := range snap {
		if !want[k] {
			t.Errorf("unexpected key %q", k)
		}
	}
}

func TestTeardownSetsStop(t *testing.T) {
	st := protocol.NewState()
	Teardown(st, make(chan *protocol.Command, 1), 100*time.Millisecond)
	if !st.Stop.IsSet() {
		t.Error("teardown should set stop")
	}
}

func TestCommandWorkerExitsOnStop(t *testing.T) {
	st := protocol.NewState()
	st.Stop.Set()
	done := make(chan struct{})
	go func() { CommandWorker(st, make(chan *protocol.Command), CommandDeadline); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("command worker did not exit on stop")
	}
}

func TestCommandWorkerDrainsOnSentinel(t *testing.T) {
	st := protocol.NewState()
	cmds := make(chan *protocol.Command, 1)
	cmds <- nil // drain sentinel
	go CommandWorker(st, cmds, CommandDeadline)
	if !st.Drained.Wait(2 * time.Second) {
		t.Error("drained should be set after the sentinel")
	}
}

func TestWatchdogExitsOnStop(t *testing.T) {
	st := protocol.NewState()
	st.Stop.Set()
	done := make(chan struct{})
	go func() { Watchdog(st, SilentAfter, ConnectWindow, DatalessAfter); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("watchdog did not exit on stop")
	}
}

func fakeProc(t *testing.T, name string, args ...string) *protocol.Proc {
	t.Helper()
	cmd := exec.Command(name, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &protocol.Proc{Cmd: cmd, Stdin: stdin, Done: make(chan struct{})}
	go func() { cmd.Wait(); close(p.Done) }()
	t.Cleanup(func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	})
	return p
}

func TestWatchdogKillsWedgedProcess(t *testing.T) {
	st := protocol.NewState()
	st.StartProc(fakeProc(t, "sleep", "60"))
	proc := st.Sproc()
	go Watchdog(st, SilentAfter, 300*time.Millisecond, DatalessAfter)
	defer st.Stop.Set()
	if !proc.WaitTimeout(3 * time.Second) {
		t.Error("watchdog should have killed the connecting-but-silent process")
	}
}

func TestWatchdogNoProcessIsHarmless(t *testing.T) {
	st := protocol.NewState()
	go Watchdog(st, 100*time.Millisecond, 500*time.Millisecond, time.Second)
	time.Sleep(150 * time.Millisecond)
	st.Stop.Set() // must not have panicked with a nil proc
}

func TestReapClosesStdinAndClears(t *testing.T) {
	st := protocol.NewState()
	proc := fakeProc(t, "cat") // cat exits on stdin EOF
	st.StartProc(proc)
	reap(st, proc)
	if st.Sproc() != nil {
		t.Error("reap should clear sproc")
	}
	if !proc.WaitTimeout(2 * time.Second) {
		t.Error("cat should exit once its stdin is closed")
	}
}

func TestReapHandlesNilStdin(t *testing.T) {
	st := protocol.NewState()
	cmd := exec.Command("true")
	cmd.Run()
	proc := &protocol.Proc{Cmd: cmd, Done: make(chan struct{})}
	close(proc.Done)
	reap(st, proc) // nil stdin + already-exited: must not panic
}

func logContains(path, sub string) bool { return strings.Contains(readFile(path), sub) }

func readFile(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}
