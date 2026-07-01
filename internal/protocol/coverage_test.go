package protocol

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"
	"time"
)

// ---- pyStr ------------------------------------------------------------------

// pyStr mirrors Python str() for non-string values landing in a string field.
func TestCov_pyStr(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{true, "True"},                           // bool true
		{false, "False"},                         // bool false
		{1.5, "1.5"},                             // float64
		{json.Number("42"), "42"},                // json.Number
		{"already a string", "already a string"}, // string passthrough
		{7, "7"},                                 // default: fmt.Sprintf("%v") on an int
		{nil, "<nil>"},                           // default: fmt.Sprintf("%v") on nil
	}
	for _, c := range cases {
		if got := pyStr(c.in); got != c.want {
			t.Errorf("pyStr(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---- Int (every case) -------------------------------------------------------

func TestCov_Int(t *testing.T) {
	// bool is never an int (matching Python's isinstance(bool) exclusion).
	if _, ok := Int(true); ok {
		t.Error("Int(true) should be not-ok")
	}
	if _, ok := Int(false); ok {
		t.Error("Int(false) should be not-ok")
	}
	// int / int64
	if v, ok := Int(5); !ok || v != 5 {
		t.Errorf("Int(int 5) = (%d, %v), want (5, true)", v, ok)
	}
	if v, ok := Int(int64(7)); !ok || v != 7 {
		t.Errorf("Int(int64 7) = (%d, %v), want (7, true)", v, ok)
	}
	// float64: truncates; NaN/Inf rejected
	if v, ok := Int(3.9); !ok || v != 3 {
		t.Errorf("Int(3.9) = (%d, %v), want (3, true)", v, ok)
	}
	if _, ok := Int(math.NaN()); ok {
		t.Error("Int(float64 NaN) should be not-ok")
	}
	if _, ok := Int(math.Inf(1)); ok {
		t.Error("Int(float64 +Inf) should be not-ok")
	}
	// float32: truncates; NaN/Inf rejected
	if v, ok := Int(float32(4.9)); !ok || v != 4 {
		t.Errorf("Int(float32 4.9) = (%d, %v), want (4, true)", v, ok)
	}
	if _, ok := Int(float32(math.NaN())); ok {
		t.Error("Int(float32 NaN) should be not-ok")
	}
	if _, ok := Int(float32(math.Inf(1))); ok {
		t.Error("Int(float32 +Inf) should be not-ok")
	}
	// json.Number: integer literal, non-integer float literal, out-of-range, garbage
	if v, ok := Int(json.Number("123")); !ok || v != 123 {
		t.Errorf("Int(json.Number 123) = (%d, %v), want (123, true)", v, ok)
	}
	if v, ok := Int(json.Number("2.9")); !ok || v != 2 {
		t.Errorf("Int(json.Number 2.9) = (%d, %v), want (2, true)", v, ok)
	}
	if _, ok := Int(json.Number("1e999")); ok {
		t.Error("Int(json.Number 1e999) should be not-ok (overflows to Inf)")
	}
	if _, ok := Int(json.Number("abc")); ok {
		t.Error("Int(json.Number abc) should be not-ok (unparseable)")
	}
	// string: valid + invalid
	if v, ok := Int("  42  "); !ok || v != 42 {
		t.Errorf("Int(\"  42  \") = (%d, %v), want (42, true)", v, ok)
	}
	if _, ok := Int("nope"); ok {
		t.Error("Int(\"nope\") should be not-ok")
	}
	// default: an unknown type
	if _, ok := Int([]string{"x"}); ok {
		t.Error("Int([]string) should be not-ok (unknown type)")
	}
}

// ---- Proc.WaitTimeout -------------------------------------------------------

func TestCov_WaitTimeout(t *testing.T) {
	// already-exited: Done is closed -> reports exited immediately.
	done := make(chan struct{})
	close(done)
	if !(&Proc{Done: done}).WaitTimeout(time.Second) {
		t.Error("WaitTimeout on a closed Done should report exited (true)")
	}
	// still running: Done stays open past d -> times out (false).
	if (&Proc{Done: make(chan struct{})}).WaitTimeout(10 * time.Millisecond) {
		t.Error("WaitTimeout should report false when the process has not exited")
	}
}

// ---- Event.Wait -------------------------------------------------------------

func TestCov_EventWait(t *testing.T) {
	// already set -> returns true without blocking.
	e := NewEvent()
	e.Set()
	if !e.Wait(time.Second) {
		t.Error("Wait on an already-set event should return true")
	}

	// set concurrently while blocked -> observes the channel close.
	e2 := NewEvent()
	go func() {
		time.Sleep(10 * time.Millisecond)
		e2.Set()
	}()
	if !e2.Wait(2 * time.Second) {
		t.Error("Wait should return true when Set arrives via the channel")
	}

	// never set, deadline elapses -> timeout branch returns IsSet() == false.
	e3 := NewEvent()
	if e3.Wait(10 * time.Millisecond) {
		t.Error("Wait should return false when an unset event times out")
	}
}

// ---- IterRecords early-break (yield returns false) --------------------------

func TestCov_IterRecordsEarlyBreak(t *testing.T) {
	// Two well-formed records; breaking after the first makes yield return false,
	// so IterRecords stops without draining the second.
	lines := []string{"@@p", "MID-Read:49 Data:1", "@@E", "@@v", "MID-Read:64 Data:2", "@@E"}
	count := 0
	for range IterRecords(feeder(lines)) {
		count++
		break
	}
	if count != 1 {
		t.Fatalf("consumed %d records before break, want 1", count)
	}
}

// ---- ApplyRecord: @@i line without '=' is skipped ---------------------------

func TestCov_ApplyRecordDevInfoSkipsNonKVLine(t *testing.T) {
	st := NewState()
	// A line lacking '=' makes strings.Cut report !ok -> continue; the surrounding
	// key=value lines still parse.
	feed := "@@i\nthis-line-has-no-equals\nnet=eth\nip=10.0.0.5\n@@E\n"
	for _, rec := range recordsFrom(splitLines(feed)) {
		ApplyRecord(st, rec)
	}
	di := st.DevInfoView()
	if di == nil || di.Net != "eth" || di.IP != "10.0.0.5" {
		t.Errorf("devinfo = %+v, want Net=eth IP=10.0.0.5 (junk line skipped)", di)
	}
}

// ---- EQ / tunnel state ------------------------------------------------------

func TestCov_ApplyTunnelAndLocalHold(t *testing.T) {
	st := NewState()
	st.ApplyTunnel("BAS", 5)
	if v, ok := st.EQValue("BAS"); !ok || v != 5 {
		t.Errorf("EQValue(BAS) = (%d, %v), want (5, true)", v, ok)
	}
	if conn, _ := st.EQView(); !conn {
		t.Error("ApplyTunnel should mark the tunnel connected")
	}

	// A local change arms the echo-suppression hold; a device echo arriving inside
	// the window is dropped (value stays at the optimistic local one).
	st.SetEQLocal("TRE", 9)
	st.ApplyTunnel("TRE", 3)
	if v, ok := st.EQValue("TRE"); !ok || v != 9 {
		t.Errorf("EQValue(TRE) = (%d, %v), want (9, true) — echo suppressed within hold", v, ok)
	}
}

func TestCov_EQAccessors(t *testing.T) {
	st := NewState()
	// Unknown control before any data.
	if _, ok := st.EQValue("MXV"); ok {
		t.Error("EQValue for an unseen control should be not-ok")
	}
	// PreloadEQ seeds values WITHOUT marking the tunnel connected.
	st.PreloadEQ(map[string]int{"MXV": 80, "VBS": 1})
	if v, ok := st.EQValue("MXV"); !ok || v != 80 {
		t.Errorf("EQValue(MXV) after preload = (%d, %v), want (80, true)", v, ok)
	}
	if conn, vals := st.EQView(); conn || vals["MXV"] != 80 || vals["VBS"] != 1 {
		t.Errorf("after PreloadEQ: EQView = (%v, %v), want (false, MXV=80 VBS=1)", conn, vals)
	}
	// SetEQConnected flips the link flag both ways.
	st.SetEQConnected(true)
	if conn, vals := st.EQView(); !conn || vals["MXV"] != 80 {
		t.Errorf("after SetEQConnected(true): EQView = (%v, %v)", conn, vals)
	}
	st.SetEQConnected(false)
	if conn, _ := st.EQView(); conn {
		t.Error("SetEQConnected(false) should clear the link state")
	}
}

// ---- Note / SetFatal / ClearFatalOnData -------------------------------------

func TestCov_NoteAndFatal(t *testing.T) {
	st := NewState()
	st.Note("transient")
	if _, _, _, msg, _ := st.DiagView(); msg != "transient" {
		t.Errorf("errMsg = %q, want \"transient\"", msg)
	}

	// SetFatal latches; Note is a no-op while fatal.
	st.SetFatal("boom")
	st.Note("ignored while fatal")
	if _, _, _, msg, _ := st.DiagView(); msg != "boom" {
		t.Errorf("errMsg = %q, want \"boom\" (Note no-ops once fatal)", msg)
	}
	if !st.Snap().Fatal {
		t.Error("Snap().Fatal should be true after SetFatal")
	}

	// ClearFatalOnData self-heals once data flows again.
	st.ClearFatalOnData()
	if st.Snap().Fatal {
		t.Error("ClearFatalOnData should clear the fatal flag")
	}
	if _, _, _, msg, _ := st.DiagView(); msg != "" {
		t.Errorf("errMsg = %q, want cleared after ClearFatalOnData", msg)
	}
}

// ---- proc / watchdog / writer accessors -------------------------------------

func TestCov_ProcAndWatchdogAccessors(t *testing.T) {
	st := NewState()
	if st.Sproc() != nil {
		t.Error("Sproc should be nil before any spawn")
	}
	if st.RawAttempts() != 0 {
		t.Errorf("RawAttempts = %d, want 0", st.RawAttempts())
	}

	p := &Proc{Done: make(chan struct{})}
	st.StartProc(p)
	if st.RawAttempts() != 1 {
		t.Errorf("RawAttempts after StartProc = %d, want 1", st.RawAttempts())
	}
	if st.Sproc() != p {
		t.Error("Sproc should return the started proc")
	}

	proc, spawned, lastRx, lastData, got := st.WatchdogView()
	if proc != p || spawned.IsZero() || !lastRx.IsZero() || !lastData.IsZero() || got {
		t.Errorf("WatchdogView = (%v, spawned=%v rx=%v data=%v got=%v), want (proc, recent, zero, zero, false)",
			proc, spawned, lastRx, lastData, got)
	}

	// A fresh spawn is writable within the live window.
	if target, live := st.WriterTarget(time.Now(), 5*time.Second); target != p || !live {
		t.Errorf("WriterTarget(fresh) = (%v, %v), want (proc, true)", target, live)
	}
	// Far past the live window with no data -> not live.
	if _, live := st.WriterTarget(time.Now().Add(time.Hour), time.Second); live {
		t.Error("WriterTarget should report not-live long after spawn with no data")
	}

	if lt, rx := st.LastTrackAndRx(); lt != nil || !rx.IsZero() {
		t.Errorf("LastTrackAndRx = (%v, %v), want (nil, zero)", lt, rx)
	}

	st.Reap()
	if st.Sproc() != nil {
		t.Error("Reap should drop the proc handle")
	}
	if st.Snap().Connected {
		t.Error("Reap should mark the connection dead")
	}
}

// ---- VolAndPremute ----------------------------------------------------------

func TestCov_VolAndPremute(t *testing.T) {
	st := NewState()
	st.SetVol(60)
	st.SetVol(0) // mute: captures pre-mute level 60
	if v, pre := st.VolAndPremute(); v != 0 || pre != 60 {
		t.Errorf("VolAndPremute = (%d, %d), want (0, 60)", v, pre)
	}
}

// ---- ToggleOptimistic -------------------------------------------------------

func TestCov_ToggleOptimistic(t *testing.T) {
	st := NewState()

	// From playing (0): reports wasPlaying=true and flips to not-playing (2).
	st.mu.Lock()
	st.playing = 0
	st.mu.Unlock()
	if wasPlaying := st.ToggleOptimistic(); !wasPlaying {
		t.Error("ToggleOptimistic from playing should report wasPlaying=true")
	}
	if p := st.Snap().Playing; p != 2 {
		t.Errorf("playing = %d, want 2 after toggling off", p)
	}

	// From not-playing: reports wasPlaying=false and flips to playing (0).
	if wasPlaying := st.ToggleOptimistic(); wasPlaying {
		t.Error("ToggleOptimistic from not-playing should report wasPlaying=false")
	}
	if p := st.Snap().Playing; p != 0 {
		t.Errorf("playing = %d, want 0 after toggling on", p)
	}
}

// ---- pingStat decreasing-sample (jitter abs) --------------------------------

func TestCov_PingStatNegativeDelta(t *testing.T) {
	// A decreasing successive sample exercises the abs branch (d < 0 -> d = -d).
	ps := pingStat([]float64{30, 10, 25})
	if want := 65.0 / 3.0; ps.Avg != want {
		t.Errorf("Avg = %v, want %v", ps.Avg, want)
	}
	// jitter = mean(|10-30|, |25-10|) = mean(20, 15) = 17.5
	if ps.Jitter != 17.5 {
		t.Errorf("Jitter = %v, want 17.5", ps.Jitter)
	}
	if ps.Peak != 30 {
		t.Errorf("Peak = %v, want 30", ps.Peak)
	}
	if !ps.OK {
		t.Error("a populated ring should read OK")
	}
}

// ---- updateNet latency-ring trim --------------------------------------------

func TestCov_UpdateNetRingTrim(t *testing.T) {
	st := NewState()
	t0 := time.Now()
	// Push 35 latency samples; the ring caps at pingRingMax (30), keeping newest.
	for i := range 35 {
		st.updateNet(&SysInfo{PingClient: strconv.Itoa(i)}, t0.Add(time.Duration(i)*time.Second))
	}
	ps := st.NetView().Ping[0]
	// 35 pushed (0..34), the newest pingRingMax (30) retained -> 5..34: the
	// average proves the trim (untrimmed 0..34 would read 17), the peak the tail.
	if want := 19.5; ps.Avg != want {
		t.Errorf("Avg = %v, want %v (ring trimmed to the newest %d)", ps.Avg, want, pingRingMax)
	}
	if ps.Peak != 34 {
		t.Errorf("Peak = %v, want 34", ps.Peak)
	}
}
