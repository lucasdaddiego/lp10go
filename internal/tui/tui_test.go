package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/fixtures"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

// applyFixtureRecords feeds every framed record of a fixture into st.
func applyFixtureRecords(st *protocol.State, name string) {
	lines := strings.Split(strings.TrimSuffix(fixtures.Get(name), "\n"), "\n")
	for rec := range protocol.IterRecords(feeder(lines)) {
		protocol.ApplyRecord(st, rec)
	}
}

// ---- controller harness -----------------------------------------------------

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

func playingRecord() protocol.Record {
	lines := strings.Split(strings.TrimSuffix(fixtures.Get("playing_record.txt"), "\n"), "\n")
	for rec := range protocol.IterRecords(feeder(lines)) {
		return rec
	}
	return nil
}

func defaultCfg() config.Config {
	return config.Config{Host: "192.168.1.40", User: "root", Name: "LP10 · Living", VolStep: 2}
}

// makeModel returns a model seeded with the playing record (playing == 0), plus
// the State and a collector that drains the command channel.
func makeModel(t *testing.T) (*model, *protocol.State, func() []protocol.Command) {
	t.Helper()
	st := protocol.NewState()
	protocol.ApplyRecord(st, playingRecord())
	return modelWith(st)
}

func modelWith(st *protocol.State) (*model, *protocol.State, func() []protocol.Command) {
	cmds := make(chan *protocol.Command, 64)
	m := newModel(st, defaultCfg(), cmds, nil)
	collect := func() []protocol.Command {
		var out []protocol.Command
		for {
			select {
			case c := <-cmds:
				if c != nil {
					out = append(out, *c)
				}
			default:
				return out
			}
		}
	}
	return m, st, collect
}

func kr(r rune) keyEvent    { return keyEvent{kind: kRune, r: r} }
func ke(k keyKind) keyEvent { return keyEvent{kind: k} }
func last(c []protocol.Command) protocol.Command {
	return c[len(c)-1]
}

// ---- controller: focus / quit / drain ---------------------------------------

func TestQuitAndFocusKeys(t *testing.T) {
	m, _, _ := makeModel(t)
	if m.key(kr('q')) != "quit" {
		t.Error("q should quit")
	}
	if m.focus != 1 {
		t.Errorf("focus = %d, want 1", m.focus)
	}
	m.key(ke(kRight))
	if m.focus != 2 {
		t.Errorf("focus = %d, want 2", m.focus)
	}
	m.key(ke(kLeft))
	m.key(ke(kLeft))
	if m.focus != 0 {
		t.Errorf("focus = %d, want 0", m.focus)
	}
	m.key(ke(kLeft))
	if m.focus != len(actions)-1 {
		t.Errorf("focus = %d, want wrap to %d", m.focus, len(actions)-1)
	}
}

func TestEnterPressesFocusedButton(t *testing.T) {
	m, _, collect := makeModel(t)
	m.focus = 2 // next
	m.key(ke(kEnter))
	got := collect()
	if len(got) != 1 || got[0].Mid != 40 || got[0].Data != "NEXT" {
		t.Errorf("sent = %+v, want [40 NEXT]", got)
	}
}

func TestToggleIsOptimistic(t *testing.T) {
	m, st, collect := makeModel(t)
	if st.Snap().Playing != 0 {
		t.Fatal("setup: should start playing")
	}
	m.key(kr(' '))
	got := collect()
	if len(got) != 1 || got[0].Mid != 40 || got[0].Data != "PAUSE" {
		t.Errorf("sent = %+v, want [40 PAUSE]", got)
	}
	if st.Snap().Playing == 0 {
		t.Error("playing should optimistically flip to not-playing")
	}
}

func TestMuteRoundTripRestoresPremute(t *testing.T) {
	m, st, collect := makeModel(t)
	st.SetVol(40)
	m.key(kr('m'))
	if c := last(collect()); c.Mid != 64 || c.Data != "0" || st.Snap().Vol != 0 {
		t.Errorf("mute: last sent should be 64 0, vol 0; got vol %d", st.Snap().Vol)
	}
	m.key(kr('m'))
	if c := last(collect()); c.Mid != 64 || c.Data != "40" {
		t.Errorf("unmute should restore premute 40, got %+v", c)
	}
}

func TestMuteWithNoHistoryUsesDefault(t *testing.T) {
	m, _, collect := modelWith(protocol.NewState()) // fresh: vol 0, no premute
	m.key(kr('m'))
	if c := last(collect()); c.Mid != 64 || c.Data != "30" {
		t.Errorf("mute with no history should use default 30, got %+v", c)
	}
}

func TestBareEscRequestsDrainAndDiagClosesOnAnyKey(t *testing.T) {
	m, _, _ := makeModel(t)
	if m.key(ke(kEsc)) != "drain" {
		t.Error("bare esc should request drain")
	}
	m.key(kr('?'))
	if !m.diag {
		t.Error("? should open diag")
	}
	if m.key(kr('q')) != "" {
		t.Error("any key closes diag (no quit)")
	}
	if m.diag {
		t.Error("diag should be closed")
	}
}

func TestVolumeArrowsStepAndFlash(t *testing.T) {
	m, st, collect := makeModel(t)
	st.SetVol(50)
	m.key(ke(kUp))
	if c := last(collect()); c.Mid != 64 || c.Data != "52" {
		t.Errorf("up should step to 52, got %+v", c)
	}
	m.key(kr('-'))
	if c := last(collect()); c.Mid != 64 || c.Data != "50" {
		t.Errorf("- should step to 50, got %+v", c)
	}
}

func TestTTogglesRemaining(t *testing.T) {
	m, _, _ := makeModel(t)
	if !m.showRemaining {
		t.Fatal("showRemaining should start true")
	}
	m.key(kr('t'))
	if m.showRemaining {
		t.Error("t should toggle showRemaining")
	}
}

func TestControllerInitialization(t *testing.T) {
	m := newModel(protocol.NewState(), defaultCfg(), make(chan *protocol.Command, 1), nil)
	if m.focus != 1 || m.diag || !m.showRemaining || len(m.flash) != 0 || m.pane != paneNow {
		t.Errorf("init state wrong: %+v", m)
	}
}

func TestControllerDoActions(t *testing.T) {
	m, st, collect := modelWith(protocol.NewState())
	m.do("next")
	if c := collect(); len(c) != 1 || c[0].Data != "NEXT" {
		t.Errorf("next: %+v", c)
	}
	m.do("prev")
	if c := collect(); len(c) != 1 || c[0].Data != "PREV" {
		t.Errorf("prev: %+v", c)
	}
	st.SetVol(50)
	m.do("volup")
	if c := collect(); len(c) != 1 || c[0].Mid != 64 || c[0].Data != "52" {
		t.Errorf("volup: %+v", c)
	}
	m.do("voldn")
	if c := collect(); len(c) != 1 || c[0].Data != "50" {
		t.Errorf("voldn: %+v", c)
	}
}

// ---- display helpers --------------------------------------------------------

func TestFmtMs(t *testing.T) {
	cases := map[int]string{0: "00:00", 211000: "03:31", -500: "00:00", 1000: "00:01", 60000: "01:00", 3661000: "61:01"}
	for in, want := range cases {
		if got := FmtMs(in); got != want {
			t.Errorf("FmtMs(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestClipEastAsianWidth(t *testing.T) {
	if Clip("abc", 10) != "abc" {
		t.Error("no clip when it fits")
	}
	if Clip("abcdef", 4) != "abc"+GL["ell"] {
		t.Errorf("Clip(abcdef,4) = %q", Clip("abcdef", 4))
	}
	if charW('漢') != 2 {
		t.Error("CJK char should be width 2")
	}
	if got := Clip("漢字漢字", 4); got != "漢"+GL["ell"] {
		t.Errorf("Clip(漢字漢字,4) = %q, want 漢%s", got, GL["ell"])
	}
	if Clip("", 5) != "" || Clip("hello", 0) != "" || Clip("hello", -1) != "" {
		t.Error("empty/zero/negative width should yield empty")
	}
}

func TestDispW(t *testing.T) {
	if DispW("hello") != 5 || DispW("") != 0 || DispW("hello world") != 11 {
		t.Error("DispW wrong")
	}
}

func TestSourceName(t *testing.T) {
	if SourceName(nil) != "" || SourceName(protocol.Track{}) != "" || SourceName(protocol.Track{"Current Source": 0}) != "" {
		t.Error("unknown source should be blank")
	}
	cases := []struct {
		t    protocol.Track
		want string
	}{
		{protocol.Track{"PlayUrl": "spotify:track:x"}, "Spotify"},
		{protocol.Track{"PlayUrl": "tidal:track:x"}, "TIDAL"},
		{protocol.Track{"PlayUrl": "airplay:x"}, "AirPlay"},
		{protocol.Track{"Current Source": 1}, "AirPlay"},
		{protocol.Track{"Current Source": 2}, "DLNA"},
		{protocol.Track{"Current Source": 3}, "Bluetooth"},
	}
	for _, c := range cases {
		if got := SourceName(c.t); got != c.want {
			t.Errorf("SourceName(%v) = %q, want %q", c.t, got, c.want)
		}
	}
}

func TestQuality(t *testing.T) {
	if got := Quality(protocol.Track{"Mime": "audio/ogg", "SampleRate": "44100"}); got != "audio/ogg · 44.1 kHz" {
		t.Errorf("quality = %q", got)
	}
	if Quality(protocol.Track{}) != "" {
		t.Error("empty track should yield empty quality")
	}
	if got := Quality(protocol.Track{"SampleRate": 44100}); !strings.Contains(got, "44.1 kHz") {
		t.Errorf("sample-rate only = %q", got)
	}
	got := Quality(protocol.Track{"Mime": "audio/flac", "SampleRate": 44100})
	if !strings.Contains(got, "audio/flac") || !strings.Contains(got, "44.1 kHz") {
		t.Errorf("both = %q", got)
	}
}

// ---- source_name on a sanitized hostile track (moved from parsing tests) ----

func TestSourceNameOnSanitizedTrack(t *testing.T) {
	block := `MID-Read:42 Data:{"Window CONTENTS": {"PlayUrl": 7, "Current Source": 4}} Length:1`
	tr, _ := protocol.ParseMB42(block)
	if SourceName(tr) != "Spotify" {
		t.Errorf("SourceName = %q, want Spotify", SourceName(tr))
	}
}

// ---- preload ----------------------------------------------------------------

func TestPreloadSnapshotIsPausedAndSanitized(t *testing.T) {
	st := protocol.NewState()
	preloadSnapshot(st, map[string]any{
		"track": map[string]any{"TrackName": "x", "Junk": struct{}{}},
		"pos":   5000, "vol": 30, "playing": 0,
	})
	s := st.Snap()
	if s.Playing != 2 {
		t.Error("never resume a cached clock (playing should be 2)")
	}
	if s.Track.Str("TrackName") != "x" {
		t.Errorf("track = %v", s.Track)
	}
	if _, ok := s.Track["Junk"]; ok {
		t.Error("unknown field should be dropped")
	}
	if lt, _ := st.LastTrackAndRx(); lt == nil {
		t.Error("last_track should be seeded")
	}
	if s.Pos != 5000 || s.Vol != 30 {
		t.Errorf("pos=%d vol=%d, want 5000/30", s.Pos, s.Vol)
	}
}

func TestPreloadSnapshotRejectsJunkValues(t *testing.T) {
	st := protocol.NewState()
	preloadSnapshot(st, map[string]any{"track": 42, "pos": "junk", "vol": nil})
	s := st.Snap()
	if s.Track != nil || s.Pos != 0 || s.Vol != 0 {
		t.Errorf("junk preload should yield empty state: %+v", s)
	}
}

func TestPreloadSnapshotNoneIsNoop(t *testing.T) {
	st := protocol.NewState()
	preloadSnapshot(st, nil)
	if s := st.Snap(); s.Track != nil || s.Pos != 0 {
		t.Errorf("nil preload should be a no-op: %+v", s)
	}
}

func TestPreloadSnapshotSeedsEQ(t *testing.T) {
	st := protocol.NewState()
	preloadSnapshot(st, map[string]any{
		"track": map[string]any{"TrackName": "x"},
		"pos":   1000, "vol": 30,
		"eq": map[string]any{
			"MXV": 100.0, "BAS": 3.0, "EQS": 1.0,
			"ZZZ": 5.0,    // unknown code -> dropped
			"TRE": "junk", // non-numeric -> dropped
			"MID": 999.0,  // out of range -> clamped to the control's max (10)
		},
	})
	if v, ok := st.EQValue("MXV"); !ok || v != 100 {
		t.Errorf("MXV = %d,%v want 100,true", v, ok)
	}
	if v, ok := st.EQValue("BAS"); !ok || v != 3 {
		t.Errorf("BAS = %d,%v want 3,true", v, ok)
	}
	if v, _ := st.EQValue("MID"); v != 10 {
		t.Errorf("MID = %d, want clamped to 10", v)
	}
	if _, ok := st.EQValue("ZZZ"); ok {
		t.Error("unknown EQ code should be dropped")
	}
	if _, ok := st.EQValue("TRE"); ok {
		t.Error("non-numeric EQ value should be dropped")
	}
	// preloaded values must NOT arm the echo hold: the device's seed overwrites
	st.ApplyTunnel("BAS", -5)
	if v, _ := st.EQValue("BAS"); v != -5 {
		t.Errorf("device seed should overwrite preloaded value, got %d", v)
	}
}

func TestPreloadSnapshotBasic(t *testing.T) {
	st := protocol.NewState()
	preloadSnapshot(st, map[string]any{
		"track": map[string]any{"TrackName": "Test"}, "pos": 1000, "vol": 50, "playing": 0,
	})
	s := st.Snap()
	if s.Track.Str("TrackName") != "Test" || s.Pos != 1000 || s.Vol != 50 || s.Playing != 2 {
		t.Errorf("preload basic wrong: %+v", s)
	}
}

// ---- input: coalesced multi-rune key batches --------------------------------

func TestTranslateAllExpandsMultiRune(t *testing.T) {
	evs := translateAll(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'+', '+', 'q'}})
	if len(evs) != 3 || evs[0].r != '+' || evs[1].r != '+' || evs[2].r != 'q' {
		t.Errorf("multi-rune batch should expand 1:1, got %+v", evs)
	}
	one := translateAll(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if len(one) != 1 || one[0].kind != kRune || one[0].r != 'm' {
		t.Errorf("single rune should pass through translate(), got %+v", one)
	}
}

// A coalesced "++" (one KeyRunes carrying two runes, as Bubble Tea delivers a
// paste or fast typing) must raise the volume twice, not be dropped.
func TestCoalescedRunesAreNotDropped(t *testing.T) {
	m, st, collect := makeModel(t)
	st.SetVol(50)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'+', '+'}})
	got := collect()
	if len(got) == 0 || last(got).Mid != 64 || last(got).Data != "54" {
		t.Errorf("++ should step volume twice 50->52->54, got %+v", got)
	}
	// a batch containing 'q' still quits
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x', 'q'}}); cmd == nil {
		t.Error("a batch containing q should still quit")
	}
}

// ---- diagnostics: on-demand resource stats ----------------------------------

func TestStatsSignalFollowsDiagOverlay(t *testing.T) {
	m, _, collect := makeModel(t)

	// closed overlay: never asks the box for stats
	for range 3 {
		m.syncStats()
	}
	if c := collect(); len(c) != 0 {
		t.Fatalf("no stats signal while diag closed, got %+v", c)
	}

	// opening it sends a single "on"
	m.diag = true
	m.syncStats()
	if c := collect(); len(c) != 1 || c[0].Mid != 90 || c[0].Data != "1" {
		t.Fatalf("diag open should send 90 1, got %+v", c)
	}

	// it does not re-send every tick — only after the re-assert interval
	// (statsTicks counts StatsReassertTicks decrements down to 0)
	for range StatsReassertTicks {
		m.syncStats()
	}
	if c := collect(); len(c) != 0 {
		t.Errorf("should not re-assert before the interval, got %+v", c)
	}
	m.syncStats() // interval elapsed -> keep-alive re-assert (survives reconnect)
	if c := collect(); len(c) != 1 || c[0].Data != "1" {
		t.Errorf("should re-assert 90 1 after the interval, got %+v", c)
	}

	// closing it sends a single "off", then goes quiet
	m.diag = false
	m.syncStats()
	if c := collect(); len(c) != 1 || c[0].Mid != 90 || c[0].Data != "0" {
		t.Fatalf("diag close should send 90 0, got %+v", c)
	}
	m.syncStats()
	if c := collect(); len(c) != 0 {
		t.Errorf("no further signal once closed, got %+v", c)
	}
}

// ---- diagnostics: the expanded readout --------------------------------------

func TestDiagShowsExpandedFields(t *testing.T) {
	st := protocol.NewState()
	applyFixtureRecords(st, "device_record.txt")  // @@i: eth link + storage
	applyFixtureRecords(st, "playing_record.txt") // @@s: temp / byte counters / pings
	m, _, _ := modelWith(st)
	m.rows, m.cols = 44, 100
	m.diag = true
	out := m.View()
	for _, want := range []string{
		"diagnostics", "link", "ethernet", "100 Mbit/s", "full duplex",
		"address", "latency", "you", "±", "storage", "any key returns",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("diag overlay missing %q", want)
		}
	}
}

// ---- layout: short-terminal overflow cap ------------------------------------

func TestDashboardDoesNotOverflowShortTerminal(t *testing.T) {
	m, _, _ := makeModel(t)
	m.cols = 80
	m.rows = 12 // compact range (9..25); the body would otherwise exceed the frame
	out := m.View()
	if n := len(strings.Split(out, "\n")); n > m.rows {
		t.Errorf("rendered %d lines into a %d-row terminal — frame overflowed", n, m.rows)
	}
}

// ---- Clip: width contract holds at degenerate widths ------------------------

func TestClipNeverExceedsWidth(t *testing.T) {
	cases := []struct {
		s string
		w int
	}{
		{"abcdef", 1}, {"abcdef", 2}, {"abcdef", 3},
		{"漢字漢字", 1}, {"漢字漢字", 2}, {"漢字漢字", 3},
		{"hello world", 1},
	}
	for _, c := range cases {
		if got := Clip(c.s, c.w); DispW(got) > c.w {
			t.Errorf("Clip(%q,%d)=%q has width %d > %d", c.s, c.w, got, DispW(got), c.w)
		}
	}
	if got := Clip("abcdef", 1); got != "a" {
		t.Errorf("Clip(abcdef,1)=%q, want a (no room for ellipsis)", got)
	}
}

// ---- now-playing marquee ----------------------------------------------------

func TestDispWindow(t *testing.T) {
	cases := []struct {
		s      string
		off, w int
		want   string
	}{
		{"abcdef", 0, 3, "abc"},
		{"abcdef", 2, 3, "cde"},
		{"abcdef", 4, 4, "ef  "}, // past the end -> padded to w
		{"ab", 0, 5, "ab   "},    // shorter than w -> padded
		{"·a·b·c", 2, 2, "·b"},   // multibyte width-1 ('·' is 1 col)
		{"abcdef", 0, 0, ""},     // zero width
	}
	for _, c := range cases {
		got := dispWindow(c.s, c.off, c.w)
		if got != c.want {
			t.Errorf("dispWindow(%q,%d,%d) = %q, want %q", c.s, c.off, c.w, got, c.want)
		}
		if c.w > 0 && DispW(got) != c.w {
			t.Errorf("dispWindow(%q,%d,%d) width = %d, want exactly %d", c.s, c.off, c.w, DispW(got), c.w)
		}
	}
}

func TestMarqueeFitsAndScrolls(t *testing.T) {
	m, _, _ := makeModel(t)

	// a line that fits is returned untouched (no padding, no scroll)
	if got := m.marquee("short line", 40); got != "short line" {
		t.Errorf("fitting line changed: %q", got)
	}

	long := "A very long album title that simply will not fit in this column"

	// at scroll 0 it pauses on the head, exactly w wide
	m.scroll = 0
	head := m.marquee(long, 20)
	if DispW(head) != 20 {
		t.Fatalf("overflow window width = %d, want 20", DispW(head))
	}
	if !strings.HasPrefix(head, "A very long album ti") {
		t.Errorf("head window = %q, want the start of the title", head)
	}

	// it stays on the head through the pause window...
	m.scroll = marqueePauseCol * marqueeColTicks
	if m.marquee(long, 20) != head {
		t.Error("should still show the head during the pause")
	}

	// ...then scrolls (a different, still-exactly-w window)
	m.scroll = (marqueePauseCol + 5) * marqueeColTicks
	scrolled := m.marquee(long, 20)
	if scrolled == head {
		t.Error("should have scrolled past the head after the pause")
	}
	if DispW(scrolled) != 20 {
		t.Errorf("scrolled window width = %d, want 20", DispW(scrolled))
	}

	// and it loops back to the head after a full cycle
	strip := long + marqueeGap
	cycle := (DispW(strip) + marqueePauseCol) * marqueeColTicks
	m.scroll = cycle
	if m.marquee(long, 20) != head {
		t.Error("should loop back to the head after one full cycle")
	}
}

// ---- latency rows -------------------------------------------------------------

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

func TestLatencyRowRendersNumbers(t *testing.T) {
	m, _, _ := modelWith(protocol.NewState())
	m.sty = newTheme() // normally lazily set on first View()
	ps := protocol.PingStat{Avg: 6.6, Jitter: 1.1, Peak: 48, OK: true}
	row := stripANSI(m.latencyRow("gw", ps))
	for _, want := range []string{"gw", "6.6 ms", "±1.1", "max 48"} {
		if !strings.Contains(row, want) {
			t.Errorf("latency row missing %q in %q", want, row)
		}
	}
	// no block glyphs: the old per-row sparkline rendered as ragged boxes on fonts
	// whose block elements don't fill the cell, so the row is plain text on purpose
	if strings.ContainsAny(row, "▁▂▃▄▅▆▇█") {
		t.Errorf("latency row should be plain text, got %q", row)
	}
}

// The numeric fields are fixed-width, so the peak column starts at the same
// display column across rows with differently-sized values — the three targets
// must line up.
func TestLatencyRowColumnsAlign(t *testing.T) {
	m, _, _ := modelWith(protocol.NewState())
	m.sty = newTheme() // normally lazily set on first View()
	rows := []string{
		stripANSI(m.latencyRow("you", protocol.PingStat{Avg: 1.2, Jitter: 0.3, Peak: 2.1, OK: true})),
		stripANSI(m.latencyRow("gw", protocol.PingStat{Avg: 11, Jitter: 6.6, Peak: 31, OK: true})),
		stripANSI(m.latencyRow("spotify", protocol.PingStat{Avg: 250, Jitter: 12, Peak: 900, OK: true})),
	}
	want := strings.Index(rows[0], "max ")
	for _, r := range rows[1:] {
		if got := strings.Index(r, "max "); got != want {
			t.Errorf("peak column at %d, want %d: %q", got, want, r)
		}
	}
	t.Logf("\n%s\n%s\n%s", rows[0], rows[1], rows[2])
}

func TestDiagLatencyBlockFullRender(t *testing.T) {
	st := protocol.NewState()
	applyFixtureRecords(st, "device_record.txt") // @@i: eth link
	// feed several @@s with a gateway spike (48) amid a ~6ms baseline
	base := "@@s\n5185 0.2 0.2 0.2 138000 221064 2 AR241CE_9243.16 Linux-5.15.137 50400 "
	type s struct{ rx, tx, you, gw, net string }
	for _, smp := range []s{
		{"1000", "500", "8", "6", "24"}, {"2000", "700", "9", "6", "25"},
		{"3000", "900", "31", "7", "26"}, {"4000", "1100", "10", "48", "29"},
		{"5000", "1300", "8", "6", "25"}, {"6000", "1500", "8", "6", "25"},
	} {
		feed := base + smp.rx + " " + smp.tx + " - - " + smp.you + " " + smp.gw + " " + smp.net + "\n@@E\n"
		for rec := range protocol.IterRecords(feeder(strings.Split(strings.TrimSuffix(feed, "\n"), "\n"))) {
			protocol.ApplyRecord(st, rec)
		}
	}
	m, _, _ := modelWith(st)
	m.rows, m.cols = 44, 100
	m.diag = true
	full := stripANSI(m.View())
	// the gateway row's peak-hold must have caught the 48ms spike
	if !strings.Contains(full, "max 48") {
		t.Error("gateway peak-hold should show the 48ms spike (max 48)")
	}
	// log the network→audio slice for eyeballing
	lines := strings.SplitSeq(full, "\n")
	for ln := range lines {
		if strings.Contains(ln, "link") || strings.Contains(ln, "address") ||
			strings.Contains(ln, "traffic") || strings.Contains(ln, "latency") ||
			strings.Contains(ln, "you") || strings.Contains(ln, "gw ") || strings.Contains(ln, "spotify") {
			t.Logf("|%s|", strings.TrimRight(ln, " "))
		}
	}
}

func TestDiagTagsDiscoveredHost(t *testing.T) {
	st := protocol.NewState()
	applyFixtureRecords(st, "device_record.txt")
	cfg := defaultCfg()
	cfg.Discovered = true
	m := newModel(st, cfg, make(chan *protocol.Command, 8), nil)
	m.rows, m.cols = 44, 100
	m.diag = true
	if !strings.Contains(stripANSI(m.View()), "mDNS") {
		t.Error("a discovered host should be tagged · mDNS on the diag host line")
	}
}

func TestDiagSilentToleratesIdleCadence(t *testing.T) {
	st := protocol.NewState()
	applyFixtureRecords(st, "playing_record.txt") // marks connected + stamps last_data
	m, _, _ := modelWith(st)
	m.sty = newTheme() // normally set on first View()
	m.rows, m.cols = 44, 100
	_, dData, _, _, _ := st.DiagView()
	snap := st.Snap()
	if !snap.Connected || dData.IsZero() {
		t.Fatal("setup: expected connected with a last_data stamp")
	}
	// a ~3s idle low-poll gap must still read healthy, not flash "LUCI silent"
	idle := stripANSI(m.renderDiag(snap, dData.Add(3500*time.Millisecond), 96))
	if strings.Contains(idle, "LUCI silent") {
		t.Errorf("3.5s idle-cadence gap should read connected, got header: %q", firstLine(idle))
	}
	// a gap beyond the watchdog's own SilentAfter should flag silence
	stale := stripANSI(m.renderDiag(snap, dData.Add(workers.SilentAfter+time.Second), 96))
	if !strings.Contains(stale, "LUCI silent") {
		t.Errorf("gap past SilentAfter should flag LUCI silent, got header: %q", firstLine(stale))
	}
}

func firstLine(s string) string {
	if before, _, ok := strings.Cut(s, "\n"); ok {
		return before
	}
	return s
}
