package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/fixtures"
)

// ---- helpers ----------------------------------------------------------------

// splitLines mirrors Python str.splitlines(): split on '\n' with no trailing
// empty element for a terminating newline.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func feeder(lines []string) func() (string, bool) {
	i := 0
	return func() (string, bool) {
		if i >= len(lines) {
			return "", false
		}
		l := lines[i]
		i++
		return l, true
	}
}

func recordsFrom(lines []string) []Record {
	var out []Record
	for rec := range IterRecords(feeder(lines)) {
		out = append(out, rec)
	}
	return out
}

func records(name string) []Record {
	return recordsFrom(splitLines(fixtures.Get(name)))
}

func bBlock(name string) string {
	return strings.Join(records(name)[0]["B"], "\n")
}

func applyFixture(name string) *State {
	st := NewState()
	ApplyRecord(st, records(name)[0])
	return st
}

// ---- iter_records -----------------------------------------------------------

func TestOneRecordPerTerminator(t *testing.T) {
	if got := len(records("playing_record.txt")); got != 1 {
		t.Fatalf("records = %d, want 1", got)
	}
}

func TestRecordHasAllSections(t *testing.T) {
	rec := records("playing_record.txt")[0]
	for _, k := range []string{"B", "p", "t", "v", "s"} {
		if _, ok := rec[k]; !ok {
			t.Errorf("missing section %q", k)
		}
	}
	if len(rec) != 5 {
		t.Errorf("section count = %d, want 5", len(rec))
	}
}

func TestHeartbeatRecordHasNoBKey(t *testing.T) {
	rec := records("heartbeat_record.txt")[0]
	if _, ok := rec["B"]; ok {
		t.Error("heartbeat must not carry a B section")
	}
	if len(rec) != 4 {
		t.Errorf("section count = %d, want 4", len(rec))
	}
}

func TestDatalessRecordIsFramedButEmpty(t *testing.T) {
	rec := records("dataless_record.txt")[0]
	for _, k := range []string{"p", "t", "v"} {
		if lines, ok := rec[k]; !ok || len(lines) != 0 {
			t.Errorf("section %q: ok=%v len=%d, want present+empty", k, ok, len(lines))
		}
	}
	if len(rec) != 3 {
		t.Errorf("section count = %d, want 3", len(rec))
	}
}

func TestTwoConsecutiveRecordsDoNotBleed(t *testing.T) {
	lines := splitLines(fixtures.Get("playing_record.txt") + fixtures.Get("heartbeat_record.txt"))
	recs := recordsFrom(lines)
	if len(recs) != 2 {
		t.Fatalf("records = %d, want 2", len(recs))
	}
	if _, ok := recs[0]["B"]; !ok {
		t.Error("first record should have B")
	}
	if _, ok := recs[1]["B"]; ok {
		t.Error("second record should not have B")
	}
}

func TestUnterminatedRecordYieldsNothing(t *testing.T) {
	var lines []string
	for _, ln := range splitLines(fixtures.Get("playing_record.txt")) {
		if ln != "@@E" {
			lines = append(lines, ln)
		}
	}
	if recs := recordsFrom(lines); len(recs) != 0 {
		t.Fatalf("records = %d, want 0", len(recs))
	}
}

func TestGarbageLinesDoNotRaiseOrCorrupt(t *testing.T) {
	text := "!!noise\n" + fixtures.Get("playing_record.txt") +
		"@@nonsense\nrandom\n" + fixtures.Get("heartbeat_record.txt")
	if recs := recordsFrom(splitLines(text)); len(recs) != 2 {
		t.Fatalf("records = %d, want 2", len(recs))
	}
}

func TestOversizedRecordIsShedNotBuffered(t *testing.T) {
	lines := []string{"@@B"}
	for i := 0; i < 100000; i++ {
		lines = append(lines, strings.Repeat("x", 80))
	}
	lines = append(lines, "@@E", "@@p", "@@E")
	recs := recordsFrom(lines)
	if len(recs) != 2 {
		t.Fatalf("records = %d, want 2", len(recs))
	}
	total := 0
	for _, v := range recs[0] {
		total += len(v)
	}
	if total > 200 {
		t.Errorf("first record buffered %d lines, want <= 200", total)
	}
}

func TestIterRecordsContinuesOnException(t *testing.T) {
	// An odd/non-track B section must not stop framing: the following well-formed
	// record is still yielded, intact.
	lines := []string{"@@B", "valid line", "@@E", "@@p", "MID-Read:49 Data:123", "@@E"}
	recs := recordsFrom(lines)
	if len(recs) != 2 {
		t.Fatalf("records = %d, want 2", len(recs))
	}
	if got := recs[1]["p"]; len(got) != 1 || got[0] != "MID-Read:49 Data:123" {
		t.Errorf("second record p-section = %v, want one MID-Read:49 line", got)
	}
}

// ---- parse_mb42 -------------------------------------------------------------

func assertIdle(t *testing.T, block string) {
	t.Helper()
	tr, idle := ParseMB42(block)
	if tr != nil || !idle {
		t.Fatalf("ParseMB42 = (%v, %v), want definitive idle (nil, true)", tr, idle)
	}
}

func assertNone(t *testing.T, block string) {
	t.Helper()
	tr, idle := ParseMB42(block)
	if tr != nil || idle {
		t.Fatalf("ParseMB42 = (%v, %v), want garbage (nil, false)", tr, idle)
	}
}

func mustTrack(t *testing.T, block string) Track {
	t.Helper()
	tr, idle := ParseMB42(block)
	if tr == nil || idle {
		t.Fatalf("ParseMB42 = (%v, %v), want a track", tr, idle)
	}
	return tr
}

func TestRealIdleCaptureIsDefinitiveIdle(t *testing.T) {
	assertIdle(t, bBlock("idle_record.txt"))
}

func TestPlayingCaptureIsATrack(t *testing.T) {
	tr := mustTrack(t, bBlock("playing_record.txt"))
	if tr.Str("TrackName") != "De Música Ligera" {
		t.Errorf("TrackName = %q", tr.Str("TrackName"))
	}
	if tr.GetInt("TotalTime") != 211000 {
		t.Errorf("TotalTime = %d", tr.GetInt("TotalTime"))
	}
	if tr["Seek"] != true {
		t.Errorf("Seek = %v, want true", tr["Seek"])
	}
	if !strings.HasPrefix(tr.Str("CoverArtUrl"), "https://") {
		t.Errorf("CoverArtUrl = %q", tr.Str("CoverArtUrl"))
	}
}

func TestNonDictJSONReturnsNone(t *testing.T) {
	assertNone(t, "MID-Read:42 Data:null Length:4")
	assertNone(t, "MID-Read:42 Data:[1,2] Length:5")
}

func TestInvalidJSONReturnsNone(t *testing.T) {
	assertNone(t, "MID-Read:42 Data:{broken Length:7")
}

func TestEmptyBlockReturnsNone(t *testing.T) {
	assertNone(t, "")
}

func TestParseMB42SanitizesHostileFields(t *testing.T) {
	block := `MID-Read:42 Data:{"Window CONTENTS": {` +
		`"TrackName": "a\u0007b", "PlayUrl": 7, "Album": null, ` +
		`"TotalTime": 1e999, "Current Source": 4}} Length:1`
	tr := mustTrack(t, block)
	if tr.Str("TrackName") != "ab" {
		t.Errorf("TrackName = %q, want \"ab\" (control char stripped)", tr.Str("TrackName"))
	}
	if tr.Str("PlayUrl") != "7" {
		t.Errorf("PlayUrl = %q, want \"7\" (coerced to str)", tr.Str("PlayUrl"))
	}
	if _, ok := tr["Album"]; ok {
		t.Error("Album (null) should be dropped")
	}
	if _, ok := tr["TotalTime"]; ok {
		t.Error("TotalTime (inf) should be dropped")
	}
}

func TestParseMB42ReturnsNoneWhenWindowContentsNotDict(t *testing.T) {
	assertNone(t, `MID-Read:42 Data:{"Window CONTENTS": "not a dict"} Length:1`)
}

func TestUnknownKeysDroppedAtBoundary(t *testing.T) {
	tr := SanitizeTrack(map[string]interface{}{
		"TrackName":    "x",
		"ChannelCount": 2,      // now whitelisted -> kept
		"TotallyBogus": "nope", // not whitelisted -> dropped
	})
	if _, ok := tr["TotallyBogus"]; ok {
		t.Error("unknown key should be dropped (whitelist, not passthrough)")
	}
	if tr.GetInt("ChannelCount") != 2 {
		t.Error("ChannelCount should be whitelisted and kept")
	}
}

func TestSanitizeTrackWithNonDict(t *testing.T) {
	if SanitizeTrack("not a dict") != nil {
		t.Error("string input should yield nil")
	}
	if SanitizeTrack([]interface{}{1, 2, 3}) != nil {
		t.Error("list input should yield nil")
	}
	if SanitizeTrack(nil) != nil {
		t.Error("nil input should yield nil")
	}
}

// ---- _int -------------------------------------------------------------------

func TestIntReturnsNoneForBool(t *testing.T) {
	if _, ok := Int(true); ok {
		t.Error("Int(true) should be not-ok")
	}
	if _, ok := Int(false); ok {
		t.Error("Int(false) should be not-ok")
	}
}

func TestIntCoercion(t *testing.T) {
	cases := []struct {
		in   interface{}
		want int
		ok   bool
	}{
		{"123", 123, true},
		{211000, 211000, true},
		{-1, -1, true},
		{nil, 0, false},
		{"x", 0, false},
		{map[string]interface{}{}, 0, false},
	}
	for _, c := range cases {
		got, ok := Int(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("Int(%v) = (%d, %v), want (%d, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestIntPreservesLargeInt64(t *testing.T) {
	// 2^53+1 is not exactly representable as float64; the ParseInt-first path
	// must keep full precision rather than round through a float.
	if v, ok := Int(json.Number("9007199254740993")); !ok || v != 9007199254740993 {
		t.Errorf("Int(9007199254740993) = (%d, %v), want exact value", v, ok)
	}
	if v, ok := Int(json.Number("211000")); !ok || v != 211000 {
		t.Errorf("Int(211000) = (%d, %v)", v, ok)
	}
	if v, ok := Int(json.Number("2.9")); !ok || v != 2 {
		t.Errorf("Int(2.9) = (%d, %v), want (2, true)", v, ok)
	}
	if _, ok := Int(json.Number("1e999")); ok {
		t.Error("Int(1e999) should be not-ok (inf)")
	}
}

func TestNumOverflowTokenDropped(t *testing.T) {
	st := NewState()
	ApplyRecord(st, recordsFrom(splitLines("@@p\nMID-Read:49 Data:99999999999999999999 Length:20\n@@E\n"))[0])
	if st.RawPos() != 0 {
		t.Errorf("RawPos = %d, want 0 (out-of-range token dropped, not saturated)", st.RawPos())
	}
}

func TestPrintableMatchesCPythonCategories(t *testing.T) {
	// non-printable == Other (C*) or Separator (Z*) except the ASCII space:
	// U+200B (Cf) and U+00A0 (Zs) are stripped; the ASCII space is kept.
	if got := printable("a\u200bb\u00a0c d"); got != "abc d" {
		t.Errorf("printable = %q, want %q", got, "abc d")
	}
	if got := printable("x\x07y\ty"); got != "xyy" {
		t.Errorf("printable should strip control chars: %q", got)
	}
}

func TestIntNonfiniteFloats(t *testing.T) {
	posInf := 1.0
	for i := 0; i < 400; i++ {
		posInf *= 10
	}
	if _, ok := Int(posInf); ok {
		t.Error("Int(inf) should be not-ok")
	}
	nan := posInf - posInf
	if _, ok := Int(nan); ok {
		t.Error("Int(nan) should be not-ok")
	}
	if v, ok := Int(2.9); !ok || v != 2 {
		t.Errorf("Int(2.9) = (%d, %v), want (2, true)", v, ok)
	}
}

// ---- apply_record -----------------------------------------------------------

func TestApplyPlayingRecord(t *testing.T) {
	s := applyFixture("playing_record.txt").Snap()
	if s.Track.Str("TrackName") != "De Música Ligera" {
		t.Errorf("TrackName = %q", s.Track.Str("TrackName"))
	}
	if s.Playing != 0 {
		t.Errorf("playing = %d, want 0", s.Playing)
	}
	if s.Vol != 44 {
		t.Errorf("vol = %d, want 44", s.Vol)
	}
	if !s.Connected {
		t.Error("should be connected")
	}
}

// The newer diag-gated @@s extras (audio chain, CPU clock, scheduler contention)
// and the @@i rootfs/DNS keys parse into SysInfo/DevInfo, and an OLDER short loop
// (no trailing fields) still parses cleanly with those fields left empty.
func TestApplyNewDiagFields(t *testing.T) {
	st := NewState()
	ApplyRecord(st, records("device_record.txt")[0])  // @@i: now carries root= and dns=
	ApplyRecord(st, records("playing_record.txt")[0]) // @@s: now carries the audio-chain tail
	_, _, _, _, si := st.DiagView()
	if si == nil {
		t.Fatal("no sysinfo parsed")
	}
	for _, c := range []struct{ got, want, name string }{
		{si.PcmState, "RUNNING", "PcmState"}, {si.BufAvail, "4834", "BufAvail"},
		{si.DacRate, "44100", "DacRate"}, {si.DacFmt, "S16_LE", "DacFmt"}, {si.DacCh, "2", "DacCh"},
		{si.BufSize, "22050", "BufSize"}, {si.CpuKHz, "1200000", "CpuKHz"}, {si.Procs, "2/237", "Procs"},
	} {
		if c.got != c.want {
			t.Errorf("SysInfo.%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if si.NoiseDBm != "" { // ethernet fixture sends "-" -> dropped to ""
		t.Errorf("NoiseDBm = %q, want \"\" (eth has no Wi-Fi noise)", si.NoiseDBm)
	}
	if dev := st.DevInfoView(); dev == nil || dev.DNS != "192.168.1.1" {
		t.Errorf("DevInfo dns = %v, want 192.168.1.1", dev)
	}
}

// An older device loop that stops at the original 17 @@s fields (no audio tail)
// must still parse — the new fields just stay empty (back-compat).
func TestApplyOldShortStatsLine(t *testing.T) {
	st := NewState()
	old := "@@s\n100.0 0.1 0.1 0.1 100000 200000 2 FW.1 Linux-5 50000 1 1 - - 1 2 3\n@@E\n"
	for rec := range IterRecords(feeder(splitLines(old))) {
		ApplyRecord(st, rec)
	}
	_, _, _, _, si := st.DiagView()
	if si == nil || si.Up != "100.0" || si.NCPU != "2" {
		t.Fatalf("short @@s should still parse base fields: %+v", si)
	}
	if si.PcmState != "" || si.BufAvail != "" || si.CpuKHz != "" || si.Procs != "" {
		t.Errorf("absent trailing fields should be empty, got state=%q avail=%q clk=%q procs=%q",
			si.PcmState, si.BufAvail, si.CpuKHz, si.Procs)
	}
}

func TestIdleMid51Value1IsNotPlaying(t *testing.T) {
	if s := applyFixture("idle_record.txt").Snap(); s.Playing == 0 {
		t.Error("MID-51 == 1 must render as not playing")
	}
}

func TestHeartbeatKeepsExistingTrack(t *testing.T) {
	st := NewState()
	ApplyRecord(st, records("playing_record.txt")[0])
	ApplyRecord(st, records("heartbeat_record.txt")[0])
	s := st.Snap()
	if s.Track.Str("TrackName") != "De Música Ligera" {
		t.Errorf("track lost on heartbeat: %v", s.Track)
	}
	if s.Pos < 31000 {
		t.Errorf("pos = %d, want >= 31000", s.Pos)
	}
}

func TestPtvFoundAfterLeadingBlankLine(t *testing.T) {
	lines := splitLines(fixtures.Get("heartbeat_record.txt"))
	var out []string
	for _, ln := range lines {
		out = append(out, ln)
		if ln == "@@p" {
			out = append(out, "")
		}
	}
	st := NewState()
	ApplyRecord(st, recordsFrom(out)[0])
	if s := st.Snap(); s.Pos < 31000 {
		t.Errorf("pos = %d, want >= 31000", s.Pos)
	}
}

func TestDatalessRecordStampsRxButNotConnected(t *testing.T) {
	st := applyFixture("dataless_record.txt")
	if st.lastRx.IsZero() {
		t.Error("last_rx should be stamped")
	}
	if st.Snap().Connected {
		t.Error("should not be connected")
	}
}

func TestPlayingRecordCarriesTrackAndSysinfo(t *testing.T) {
	st := applyFixture("playing_record.txt")
	if st.Snap().Track == nil {
		t.Error("track should be set")
	}
	if st.sysinfo == nil || st.sysinfo.FW != "AR241CE_9243.16" {
		t.Errorf("sysinfo = %v", st.sysinfo)
	}
}

func TestVolHoldSuppressesDeviceEcho(t *testing.T) {
	st := NewState()
	ApplyRecord(st, records("playing_record.txt")[0]) // device: 44
	st.SetVol(20)                                     // arms vol_hold
	ApplyRecord(st, records("heartbeat_record.txt")[0])
	if s := st.Snap(); s.Vol != 20 {
		t.Errorf("vol = %d, want 20 (echo suppressed)", s.Vol)
	}
}

func TestPlayHoldSuppressesDeviceEcho(t *testing.T) {
	st := NewState()
	ApplyRecord(st, records("playing_record.txt")[0])
	st.mu.Lock()
	st.playing = 2
	st.playHold = time.Now().Add(5 * time.Second)
	st.mu.Unlock()
	ApplyRecord(st, records("heartbeat_record.txt")[0]) // echoes 0
	if s := st.Snap(); s.Playing != 2 {
		t.Errorf("playing = %d, want 2 (echo suppressed)", s.Playing)
	}
}

func TestGarbageBIsDebouncedWithin3s(t *testing.T) {
	st := NewState()
	ApplyRecord(st, records("playing_record.txt")[0])
	ApplyRecord(st, recordsFrom(splitLines("@@B\nnot json\n@@E\n"))[0])
	if st.Snap().Track == nil {
		t.Error("track should be kept (clear is debounced)")
	}
}

func TestGarbageBClearsAfterDebounceWindow(t *testing.T) {
	st := NewState()
	ApplyRecord(st, records("playing_record.txt")[0])
	st.mu.Lock()
	st.trackAt = time.Now().Add(-4 * time.Second)
	st.mu.Unlock()
	ApplyRecord(st, recordsFrom(splitLines("@@B\nnot json\n@@E\n"))[0])
	if st.Snap().Track != nil {
		t.Error("track should be cleared after the debounce window")
	}
}

func TestIdleBClearsImmediatelyEvenWithinDebounce(t *testing.T) {
	st := NewState()
	ApplyRecord(st, records("playing_record.txt")[0])
	ApplyRecord(st, records("idle_record.txt")[0])
	if st.Snap().Track != nil {
		t.Error("idle B should clear the track immediately")
	}
	if st.lastTrack == nil {
		t.Error("last_track should survive idle for the idle screen")
	}
}

func TestRealPlayingCaptureParses(t *testing.T) {
	tr := mustTrack(t, bBlock("playing_record_real.txt"))
	if tr.Str("TrackName") != "Cause We've Ended as Lovers" {
		t.Errorf("TrackName = %q", tr.Str("TrackName"))
	}
	if tr.Str("Artist") != "Jeff Beck" {
		t.Errorf("Artist = %q", tr.Str("Artist"))
	}
	if tr.GetInt("TotalTime") != 341535 {
		t.Errorf("TotalTime = %d", tr.GetInt("TotalTime"))
	}
	if tr["Seek"] != true {
		t.Errorf("Seek = %v, want true", tr["Seek"])
	}
	if !strings.HasPrefix(tr.Str("CoverArtUrl"), "https://i.scdn.co/") {
		t.Errorf("CoverArtUrl = %q", tr.Str("CoverArtUrl"))
	}
}

func TestRealPlayingRecordApplies(t *testing.T) {
	s := applyFixture("playing_record_real.txt").Snap()
	if s.Playing != 0 || s.Vol != 50 {
		t.Errorf("playing=%d vol=%d, want 0/50", s.Playing, s.Vol)
	}
	if s.Pos < 229457 {
		t.Errorf("pos = %d, want >= 229457", s.Pos)
	}
}

func TestSysinfoSectionParsesButIsNotLuciData(t *testing.T) {
	st := applyFixture("heartbeat_record.txt")
	if st.sysinfo.FW != "AR241CE_9243.16" || st.sysinfo.NCPU != "2" || st.sysinfo.OS != "Linux-5.15.137" {
		t.Errorf("sysinfo = %+v", st.sysinfo)
	}
	st2 := NewState()
	ApplyRecord(st2, recordsFrom(splitLines("@@s\n1.0 0 0 0 1 2 2 fw\n@@E\n"))[0])
	if st2.lastRx.IsZero() {
		t.Error("framed record should stamp last_rx")
	}
	if st2.Snap().Connected {
		t.Error("/proc data is not LUCI data; must not mark connected")
	}
}

func TestDevInfoAndSysinfoExtras(t *testing.T) {
	st := NewState()
	feed := "@@i\nnet=eth\niface=eth0\nip=192.168.1.13\nmac=aa:bb:cc:dd:ee:ff\nspeed=100\nduplex=full\nbuild=2025-12-24\napp=312\nplatform=LS8\ndata=11424 232924\n@@E\n" +
		"@@s\n100 0.5 0.4 0.3 137000 215000 2 AR241CE_9243.16 Linux-5.15.137 43200 1000 500 - - 2.1 14.3 31.4\n@@E\n"
	for _, rec := range recordsFrom(splitLines(feed)) {
		ApplyRecord(st, rec)
	}
	di := st.DevInfoView()
	if di == nil || di.Net != "eth" || di.Iface != "eth0" || di.IP != "192.168.1.13" || di.Speed != "100" || di.Duplex != "full" || di.Platform != "LS8" || di.DataUsed != "11424" || di.DataTotal != "232924" {
		t.Errorf("devinfo = %+v", di)
	}
	si := st.sysinfo
	if si == nil || si.TempmC != "43200" || si.RxBytes != "1000" || si.TxBytes != "500" || si.PingClient != "2.1" || si.PingGw != "14.3" || si.PingNet != "31.4" {
		t.Errorf("sysinfo extras = %+v", si)
	}
	if si.SignalDBm != "" || si.LinkQ != "" { // '-' placeholders: no Wi-Fi stats on ethernet
		t.Errorf("ethernet should carry empty wifi stats: %+v", si)
	}
	if si.FW != "AR241CE_9243.16" || si.OS != "Linux-5.15.137" {
		t.Errorf("base sysinfo regressed: %+v", si)
	}
}

func TestDevInfoWifiPath(t *testing.T) {
	st := NewState()
	feed := "@@i\nnet=wifi\niface=wlan0\nip=192.168.1.20\nmac=aa:bb:cc:dd:ee:f1\ngw=192.168.1.1\nssid=MyWiFi 5G\nfreq=5180\nrate=780\ndata=1 2\n@@E\n" +
		"@@s\n100 0 0 0 1 2 2 fw kt-kr 40000 100 200 -55 63 2.0 5.0 40.0\n@@E\n"
	for _, rec := range recordsFrom(splitLines(feed)) {
		ApplyRecord(st, rec)
	}
	di := st.DevInfoView()
	if di == nil || di.Net != "wifi" || di.Iface != "wlan0" || di.SSID != "MyWiFi 5G" || di.Freq != "5180" || di.Rate != "780" {
		t.Errorf("wifi devinfo = %+v", di)
	}
	if si := st.sysinfo; si == nil || si.SignalDBm != "-55" || si.LinkQ != "63" {
		t.Errorf("wifi sysinfo = %+v", si)
	}
}

func TestConfInfoCapabilities(t *testing.T) {
	st := NewState()
	if st.ConfView() != nil {
		t.Fatal("ConfView should be nil before any @@c block")
	}
	// Includes alexa/roon/matter (LibreWireless baggage the controller no longer
	// reads) and a bogus key — all must be dropped, leaving only the 8 marketed ones.
	feed := "@@c\nspotify=on\nairplay=on\ndlna=on\nbt=on\ncast=off\ntidal=off\n" +
		"qobuz=off\nusb=off\nroon=off\nalexa=off\nmatter=off\nbogus=on\n@@E\n"
	for _, rec := range recordsFrom(splitLines(feed)) {
		ApplyRecord(st, rec)
	}
	cv := st.ConfView()
	if cv == nil {
		t.Fatal("ConfView nil after @@c block")
	}
	if cv.Svc["spotify"] != "on" || cv.Svc["bt"] != "on" || cv.Svc["cast"] != "off" || cv.Svc["usb"] != "off" {
		t.Errorf("capability states wrong: %+v", cv.Svc)
	}
	for _, dropped := range []string{"bogus", "alexa", "roon", "matter"} {
		if _, ok := cv.Svc[dropped]; ok {
			t.Errorf("non-allowlisted capability %q must be dropped at the parse boundary", dropped)
		}
	}
	if len(cv.Svc) != 8 {
		t.Errorf("want the 8 allowlisted services, got %d: %+v", len(cv.Svc), cv.Svc)
	}
	// An empty value (the device couldn't read the flag) parses as "" (unknown),
	// not dropped — so the view can distinguish "off" from "not yet known".
	for _, rec := range recordsFrom(splitLines("@@c\nspotify=\n@@E\n")) {
		ApplyRecord(st, rec)
	}
	if cv := st.ConfView(); cv == nil {
		t.Fatal("ConfView nil after second @@c")
	} else if v, ok := cv.Svc["spotify"]; !ok || v != "" {
		t.Errorf("empty capability value should parse as unknown, got (%q, %v)", v, ok)
	}
}

// A malformed @@c line with no '=' is skipped at the parse boundary (the
// strings.Cut ok==false branch), a duplicate key takes the last value, and
// surrounding valid keys are unaffected — no panic.
func TestConfInfoMalformedAndDuplicateLines(t *testing.T) {
	st := NewState()
	feed := "@@c\ngarbage-no-equals\nspotify=on\nspotify=off\nbt=on\n@@E\n"
	for _, rec := range recordsFrom(splitLines(feed)) {
		ApplyRecord(st, rec)
	}
	cv := st.ConfView()
	if cv == nil {
		t.Fatal("ConfView nil after @@c block")
	}
	if v, ok := cv.Svc["spotify"]; !ok || v != "off" {
		t.Errorf("duplicate key should take the last value: got (%q, %v), want (off, true)", v, ok)
	}
	if cv.Svc["bt"] != "on" {
		t.Errorf("a valid key after a junk line should still parse: bt=%q", cv.Svc["bt"])
	}
	if _, ok := cv.Svc["garbage-no-equals"]; ok {
		t.Error("a line with no '=' must be skipped, not stored")
	}
	if len(cv.Svc) != 2 { // spotify + bt only
		t.Errorf("want 2 parsed keys (spotify, bt), got %d: %+v", len(cv.Svc), cv.Svc)
	}
}

func TestNetThroughputAndLatency(t *testing.T) {
	st := NewState()
	t0 := time.Now()
	st.updateNet(&SysInfo{RxBytes: "1000", TxBytes: "500", PingGw: "14"}, t0)
	n := st.NetView()
	if n.RatesOK {
		t.Error("rates must wait for a second sample")
	}
	if g := n.Ping[1]; !g.OK || g.Avg != 14 || g.Jitter != 0 {
		t.Errorf("gateway ping after one sample = %+v", g)
	}
	// +8000 rx, +2000 tx over 2s -> 4000 / 1000 B/s
	st.updateNet(&SysInfo{RxBytes: "9000", TxBytes: "2500", PingGw: "20"}, t0.Add(2*time.Second))
	n = st.NetView()
	if !n.RatesOK || n.RxRate != 4000 || n.TxRate != 1000 {
		t.Errorf("rates = %v / %v, want 4000 / 1000", n.RxRate, n.TxRate)
	}
	if g := n.Ping[1]; g.Avg != 17 || g.Jitter != 6 { // mean(14,20)=17, |20-14|=6
		t.Errorf("gateway ping = %+v, want avg 17 jitter 6", g)
	}
	if g := n.Ping[1]; g.Peak != 20 || len(g.Series) != 2 || g.Series[1] != 20 {
		t.Errorf("gateway peak/series = %v / %v, want peak 20, series [14 20]", g.Peak, g.Series)
	}
	// a counter reset (reboot / interface flap) must not spike the rate
	st.updateNet(&SysInfo{RxBytes: "10", TxBytes: "5"}, t0.Add(4*time.Second))
	if n := st.NetView(); n.RxRate != 4000 {
		t.Errorf("counter reset should skip the rate, got %v", n.RxRate)
	}
	// a reconnect clears the latency rings and the throughput baseline
	st.StartProc(&Proc{})
	if n := st.NetView(); n.RatesOK || n.Ping[1].OK {
		t.Errorf("reconnect should reset net stats, got %+v", n)
	}
}

func TestSysinfoExtrasPlaceholderIsEmpty(t *testing.T) {
	st := NewState()
	ApplyRecord(st, recordsFrom(splitLines("@@s\n1 0 0 0 1 2 2 fw kt-kr -\n@@E\n"))[0])
	if si := st.sysinfo; si == nil || si.TempmC != "" {
		t.Errorf("'-' placeholder should parse as empty, got %+v", si)
	}
}

func TestDeviceRecordFixtureParses(t *testing.T) {
	st := NewState()
	for _, rec := range records("device_record.txt") {
		ApplyRecord(st, rec)
	}
	di := st.DevInfoView()
	if di == nil || di.Net != "eth" || di.Iface != "eth0" || di.IP != "192.168.1.13" || di.Speed != "100" || di.Duplex != "full" ||
		di.DataUsed != "1258291" || di.DataTotal != "7340032" {
		t.Errorf("devinfo = %+v", di)
	}
}

// A section that floods past maxRecLines is shed on its own; the record's other
// well-formed sections survive (regression for the un-reset line counter).
func TestFloodingSectionDoesNotShedSiblings(t *testing.T) {
	var b strings.Builder
	b.WriteString("@@B\n")
	for i := 0; i < maxRecLines+50; i++ {
		b.WriteString("garbage line\n")
	}
	b.WriteString("@@p\nMID-Read:49 Data:4242 Length:4\n")
	b.WriteString("@@v\nMID-Read:64 Data:37 Length:2\n@@E\n")
	rec := recordsFrom(splitLines(b.String()))[0]
	if len(rec["B"]) != 0 {
		t.Errorf("flooding B section should be shed, got %d lines", len(rec["B"]))
	}
	if len(rec["p"]) == 0 || len(rec["v"]) == 0 {
		t.Errorf("sibling sections after a flood must survive: p=%v v=%v", rec["p"], rec["v"])
	}
	st := NewState()
	ApplyRecord(st, rec)
	if st.Snap().Vol != 37 {
		t.Errorf("volume from the surviving @@v section should apply, got %d", st.Snap().Vol)
	}
}

// A content-free @@B header (no track JSON) must not drive a track clear.
func TestEmptyBSectionIsNotATrackClear(t *testing.T) {
	st := NewState()
	st.track = Track{"TrackName": "keep me"}
	st.trackAt = time.Now()
	ApplyRecord(st, recordsFrom(splitLines("@@B\n@@p\nMID-Read:49 Data:10 Length:2\n@@E\n"))[0])
	if st.Snap().Track == nil {
		t.Error("an empty @@B section should leave the current track untouched")
	}
}

func TestStaleCachedTrackClearedByIdleRecord(t *testing.T) {
	st := NewState()
	st.track = Track{"TrackName": "ghost"}
	st.trackAt = time.Time{}
	ApplyRecord(st, records("idle_record.txt")[0])
	if st.Snap().Track != nil {
		t.Error("cached preload must not debounce away a real idle clear")
	}
}

// ---- State semantics --------------------------------------------------------

func playingState() *State { return applyFixture("playing_record.txt") }

func TestPositionExtrapolatesWhilePlayingAndConnected(t *testing.T) {
	st := playingState()
	p0 := st.Snap().Pos
	time.Sleep(50 * time.Millisecond)
	if st.Snap().Pos <= p0 {
		t.Error("position should advance while playing+connected")
	}
}

func TestPositionFrozenWhileDisconnected(t *testing.T) {
	st := playingState()
	st.mu.Lock()
	st.connected = false
	st.mu.Unlock()
	p0 := st.Snap().Pos
	time.Sleep(50 * time.Millisecond)
	if st.Snap().Pos != p0 {
		t.Error("position must not tick during an outage")
	}
}

func TestPositionClampedToTotalTime(t *testing.T) {
	st := playingState()
	st.mu.Lock()
	st.posMs = 10_000_000
	st.mu.Unlock()
	if s := st.Snap(); s.Pos > 211000 {
		t.Errorf("pos = %d, want <= 211000", s.Pos)
	}
}

func TestSetVolClampsAndReturns(t *testing.T) {
	st := playingState()
	if v := st.SetVol(150); v != 100 {
		t.Errorf("SetVol(150) = %d, want 100", v)
	}
	if v := st.SetVol(-5); v != 0 {
		t.Errorf("SetVol(-5) = %d, want 0", v)
	}
}

func TestPremuteSavedOnAnyTransitionToZero(t *testing.T) {
	st := playingState()
	st.SetVol(44)
	st.SetVol(0)
	if st.premute != 44 {
		t.Errorf("premute = %d, want 44", st.premute)
	}
}

func TestAdjustVolIsDeltaBased(t *testing.T) {
	st := playingState()
	st.SetVol(50)
	if v := st.AdjustVol(+2); v != 52 {
		t.Errorf("AdjustVol(+2) = %d, want 52", v)
	}
	if v := st.AdjustVol(-4); v != 48 {
		t.Errorf("AdjustVol(-4) = %d, want 48", v)
	}
}

func TestExternalResumeDoesNotJumpByPauseDuration(t *testing.T) {
	st := playingState()
	st.mu.Lock()
	st.playing = 2
	st.posMs = 60000
	st.posAt = time.Now().Add(-600 * time.Second)
	st.playHold = time.Time{}
	st.mu.Unlock()
	ApplyRecord(st, recordsFrom(splitLines("@@t\nMID-Read:51 Data:0 Length:1\n@@E\n"))[0])
	s := st.Snap()
	if s.Playing != 0 {
		t.Errorf("playing = %d, want 0", s.Playing)
	}
	if s.Pos >= 62000 {
		t.Errorf("pos = %d, want < 62000 (clock restarts at last pos)", s.Pos)
	}
}

func TestStopIsAnEvent(t *testing.T) {
	st := NewState()
	if st.Stop == nil || st.Stop.IsSet() {
		t.Error("stop should be an unset Event")
	}
	st.Stop.Set()
	if !st.Stop.IsSet() {
		t.Error("stop should be set after Set()")
	}
}

func TestSetVolPersistsPremute(t *testing.T) {
	st := NewState()
	st.PremuteFile = t.TempDir() + "/premute"
	st.SetVol(50)
	st.SetVol(0)
	if got := config.LoadPremute(st.PremuteFile); got != 50 {
		t.Errorf("persisted premute = %d, want 50", got)
	}
}

func TestAdjustVolPersistsPremute(t *testing.T) {
	st := NewState()
	st.PremuteFile = t.TempDir() + "/premute"
	st.SetVol(50)
	st.AdjustVol(-50)
	if got := config.LoadPremute(st.PremuteFile); got != 50 {
		t.Errorf("persisted premute = %d, want 50", got)
	}
}

// ---- command reduction & validation -----------------------------------------

func cmds(items ...[2]string) []Command {
	var out []Command
	for _, it := range items {
		mid := 0
		switch it[0] {
		case "64":
			mid = 64
		case "40":
			mid = 40
		case "49":
			mid = 49
		case "90":
			mid = 90
		case "99":
			mid = 99
		}
		out = append(out, Command{Mid: mid, Data: it[1]})
	}
	return out
}

func eqCmds(t *testing.T, got, want []Command) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i].Mid != want[i].Mid || got[i].Data != want[i].Data || !got[i].TS.Equal(want[i].TS) {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestLastVolumeWins(t *testing.T) {
	out := ReduceCommands(cmds([2]string{"64", "10"}, [2]string{"64", "20"}, [2]string{"64", "35"}))
	eqCmds(t, out, []Command{{Mid: 64, Data: "35"}})
}

func TestStatsToggleCollapsesToLast(t *testing.T) {
	// repeated stats re-asserts (and a final off) collapse to the last value,
	// without disturbing an interleaved transport command
	out := ReduceCommands(cmds([2]string{"90", "1"}, [2]string{"40", "NEXT"}, [2]string{"90", "1"}, [2]string{"90", "0"}))
	eqCmds(t, out, []Command{{Mid: 40, Data: "NEXT"}, {Mid: 90, Data: "0"}})
}

func TestVolumeCoalescingDoesNotReorderTransport(t *testing.T) {
	out := ReduceCommands(cmds([2]string{"64", "10"}, [2]string{"40", "NEXT"}, [2]string{"64", "20"}))
	nextIdx, volIdx := -1, -1
	for i, c := range out {
		if c.Mid == 40 && c.Data == "NEXT" {
			nextIdx = i
		}
		if c.Mid == 64 && c.Data == "20" {
			volIdx = i
		}
	}
	if nextIdx < 0 || volIdx < 0 || nextIdx >= volIdx {
		t.Errorf("NEXT(%d) must precede vol 20(%d): %v", nextIdx, volIdx, out)
	}
}

func TestPauseResumeRunCollapsesToFinal(t *testing.T) {
	out := ReduceCommands(cmds([2]string{"40", "PAUSE"}, [2]string{"40", "RESUME"}, [2]string{"40", "PAUSE"}))
	eqCmds(t, out, []Command{{Mid: 40, Data: "PAUSE"}})
}

func TestNextPrevAlwaysPreserved(t *testing.T) {
	out := ReduceCommands(cmds([2]string{"40", "NEXT"}, [2]string{"40", "NEXT"}, [2]string{"40", "PREV"}))
	eqCmds(t, out, []Command{{Mid: 40, Data: "NEXT"}, {Mid: 40, Data: "NEXT"}, {Mid: 40, Data: "PREV"}})
}

func TestMixedBurst(t *testing.T) {
	out := ReduceCommands(cmds(
		[2]string{"40", "PAUSE"}, [2]string{"64", "10"}, [2]string{"40", "RESUME"},
		[2]string{"64", "30"}, [2]string{"40", "NEXT"}))
	eqCmds(t, out, []Command{
		{Mid: 40, Data: "PAUSE"}, {Mid: 40, Data: "RESUME"},
		{Mid: 64, Data: "30"}, {Mid: 40, Data: "NEXT"}})
}

func TestReduceKeepsEachSurvivorsOwnTimestamp(t *testing.T) {
	ts := func(n int64) time.Time { return time.Unix(n, 0) }
	in := []Command{
		{Mid: 64, Data: "10", TS: ts(1)},
		{Mid: 40, Data: "PAUSE", TS: ts(2)},
		{Mid: 40, Data: "RESUME", TS: ts(3)},
		{Mid: 64, Data: "35", TS: ts(4)},
	}
	out := ReduceCommands(in)
	eqCmds(t, out, []Command{
		{Mid: 40, Data: "RESUME", TS: ts(3)},
		{Mid: 64, Data: "35", TS: ts(4)},
	})
}

func TestPayloadWhitelist(t *testing.T) {
	cases := []struct {
		mid  int
		data string
		want bool
	}{
		{40, "PAUSE", true},
		{40, "NEXT", true},
		{40, "PAUSE; rm -rf /", false},
		{64, "0", true},
		{64, "100", true},
		{64, "101", false},
		{64, "-1", false},
		{49, "154000", false},
		{99, "1", false},
		{90, "1", true},  // diagnostics-stats toggle on
		{90, "0", true},  // off
		{90, "2", false}, // only 0/1 reach the device
		{90, "", false},
	}
	for _, c := range cases {
		if got := ValidatePayload(c.mid, c.data); got != c.want {
			t.Errorf("ValidatePayload(%d, %q) = %v, want %v", c.mid, c.data, got, c.want)
		}
	}
}
