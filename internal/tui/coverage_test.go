package tui

import (
	"image/color"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

// ============================================================================
// Pure display/formatting helpers — called directly, every branch.
// ============================================================================

func TestCov_freqToChan(t *testing.T) {
	cases := map[int]int{2484: 14, 2412: 1, 2437: 6, 5180: 36, 0: 0}
	for in, want := range cases {
		if got := freqToChan(in); got != want {
			t.Errorf("freqToChan(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestCov_pingLabel(t *testing.T) {
	cases := map[string]string{
		"":                       "net",
		"1.2.3.4":                "1.2.3.4",
		"apresolve.spotify.com":  "spotify",
		"spotify.com":            "spotify",
		"localhost":              "localhost",
		"  apresolve.tidal.com ": "tidal",
	}
	for in, want := range cases {
		if got := pingLabel(in); got != want {
			t.Errorf("pingLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCov_fmtRate(t *testing.T) {
	cases := map[float64]string{
		512:     "512 B/s",
		2048:    "2 KB/s",
		3 << 20: "3.0 MB/s",
	}
	for in, want := range cases {
		if got := fmtRate(in); got != want {
			t.Errorf("fmtRate(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestCov_fmtKHz(t *testing.T) {
	if got := fmtKHz(44100); got != "44.1 kHz" {
		t.Errorf("fmtKHz(44100) = %q, want 44.1 kHz", got)
	}
	if got := fmtKHz(48000); got != "48 kHz" {
		t.Errorf("fmtKHz(48000) = %q, want 48 kHz", got)
	}
}

func TestCov_fmtUptime(t *testing.T) {
	cases := map[string]string{
		"":      "—",
		"abc":   "—",
		"-5":    "—",
		"30":    "0m",
		"3700":  "1h 1m",
		"90061": "1d 1h 1m",
	}
	for in, want := range cases {
		if got := fmtUptime(in); got != want {
			t.Errorf("fmtUptime(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCov_fmtLatencyMs(t *testing.T) {
	if got := fmtLatencyMs(5.5); got != "5.5" {
		t.Errorf("fmtLatencyMs(5.5) = %q, want 5.5", got)
	}
	if got := fmtLatencyMs(25.4); got != "25" {
		t.Errorf("fmtLatencyMs(25.4) = %q, want 25", got)
	}
}

func TestCov_orDashFirstSegToneStr(t *testing.T) {
	if orDash("") != "—" || orDash("x") != "x" {
		t.Error("orDash wrong")
	}
	if firstSeg("AR241CE_9243", '_') != "AR241CE" {
		t.Error("firstSeg with sep wrong")
	}
	if firstSeg("nosep", '_') != "nosep" {
		t.Error("firstSeg without sep should pass through")
	}
	if toneStr(0) != "0" || toneStr(3) != "+3" || toneStr(-6) != "-6" {
		t.Errorf("toneStr wrong: %q %q %q", toneStr(0), toneStr(3), toneStr(-6))
	}
}

func TestCov_padHelpers(t *testing.T) {
	if got := rpadDisp("ab", 5); got != "   ab" {
		t.Errorf("rpadDisp(ab,5) = %q", got)
	}
	if got := rpadDisp("abcde", 3); got != "abcde" {
		t.Errorf("rpadDisp wide no-op = %q", got)
	}
	if got := padDisp("ab", 5); got != "ab   " {
		t.Errorf("padDisp(ab,5) = %q", got)
	}
	if got := padDisp("abcde", 3); got != "abcde" {
		t.Errorf("padDisp wide no-op = %q", got)
	}
	if got := padVis("ab", 5); got != "ab   " {
		t.Errorf("padVis(ab,5) = %q", got)
	}
	if got := padVis("abcde", 3); got != "abcde" {
		t.Errorf("padVis wide no-op = %q", got)
	}
	if got := labelGap("ab", 5); got != "   " {
		t.Errorf("labelGap(ab,5) = %q (len %d)", got, len(got))
	}
	if got := labelGap("abcdef", 3); got != "" {
		t.Errorf("labelGap overflow should be empty, got %q", got)
	}
}

func TestCov_betweenSplitCcell(t *testing.T) {
	if got := between("L", 1, "R", 1, 10); got != "L"+strings.Repeat(" ", 8)+"R" {
		t.Errorf("between normal = %q", got)
	}
	// gap < 1 clamps to a single space
	if got := between("L", 5, "R", 5, 8); got != "L R" {
		t.Errorf("between clamp = %q, want %q", got, "L R")
	}
	if got := splitWidth(10, 3); len(got) != 3 || got[0] != 4 || got[1] != 3 || got[2] != 3 {
		t.Errorf("splitWidth(10,3) = %v, want [4 3 3]", got)
	}
	if got := splitWidth(9, 3); got[0] != 3 || got[1] != 3 || got[2] != 3 {
		t.Errorf("splitWidth(9,3) = %v, want [3 3 3]", got)
	}
	if w := lipgloss.Width(ccell("x", 5)); w != 5 {
		t.Errorf("ccell width = %d, want 5", w)
	}
}

func TestCov_dispWindowEdges(t *testing.T) {
	// content entirely before the window: a,b,c skipped, "de" taken
	if got := dispWindow("abcdef", 3, 2); got != "de" {
		t.Errorf("dispWindow(abcdef,3,2) = %q, want de", got)
	}
	// a double-width rune straddling each edge renders its visible cells as spaces
	got := dispWindow("漢字漢字", 1, 4)
	if DispW(got) != 4 {
		t.Errorf("dispWindow straddle width = %d, want 4", DispW(got))
	}
	if !strings.Contains(got, "字") {
		t.Errorf("dispWindow straddle should keep the fully-inside 字: %q", got)
	}
}

func TestCov_sourceNameSources(t *testing.T) {
	cases := []struct {
		src  int
		want string
	}{{4, "Spotify"}, {5, "Line-In"}, {6, "USB"}, {7, "Source 7"}}
	for _, c := range cases {
		if got := SourceName(protocol.Track{"Current Source": c.src}); got != c.want {
			t.Errorf("SourceName(src %d) = %q, want %q", c.src, got, c.want)
		}
	}
}

func TestCov_glyphsAndDetectAmb(t *testing.T) {
	// glyphs(2) is the ASCII fallback set (CJK locale / ambiguous-wide terminal).
	if g := glyphs(2); g["play"] != ">" || g["note"] != "*" || g["ell"] != "..." {
		t.Errorf("glyphs(2) ASCII fallback wrong: %v", g["play"])
	}
	if g := glyphs(1); g["play"] != "▶" || g["note"] != "♪" {
		t.Errorf("glyphs(1) unicode set wrong: %v", g["play"])
	}

	// detectAmb: CJK locales -> 2, everything else -> 1, with LC_ALL > LC_CTYPE > LANG.
	for _, lang := range []string{"ja_JP.UTF-8", "ko_KR.UTF-8", "zh_CN.UTF-8"} {
		t.Run(lang, func(t *testing.T) {
			t.Setenv("LC_ALL", "")
			t.Setenv("LC_CTYPE", "")
			t.Setenv("LANG", lang)
			if got := detectAmb(); got != 2 {
				t.Errorf("detectAmb(%s) = %d, want 2", lang, got)
			}
		})
	}
	t.Run("default", func(t *testing.T) {
		t.Setenv("LC_ALL", "")
		t.Setenv("LC_CTYPE", "")
		t.Setenv("LANG", "en_US.UTF-8")
		if got := detectAmb(); got != 1 {
			t.Errorf("detectAmb(en) = %d, want 1", got)
		}
	})
	t.Run("lc_all_precedence", func(t *testing.T) {
		t.Setenv("LC_ALL", "ja_JP.UTF-8")
		t.Setenv("LC_CTYPE", "en_US.UTF-8")
		t.Setenv("LANG", "en_US.UTF-8")
		if got := detectAmb(); got != 2 {
			t.Errorf("detectAmb(LC_ALL=ja) = %d, want 2 (LC_ALL wins)", got)
		}
	})
}

// ============================================================================
// theme.go pure helpers
// ============================================================================

func TestCov_clampFClampRangeTo8(t *testing.T) {
	if clampF(-0.5) != 0 || clampF(0.5) != 0.5 || clampF(1.5) != 1 {
		t.Error("clampF wrong")
	}
	if clampRange(0.1, 0.35, 0.85) != 0.35 {
		t.Error("clampRange below")
	}
	if clampRange(0.5, 0.35, 0.85) != 0.5 {
		t.Error("clampRange in")
	}
	if clampRange(0.99, 0.35, 0.85) != 0.85 {
		t.Error("clampRange above")
	}
	if to8(-0.1) != 0 || to8(0.5) != 128 || to8(2) != 255 {
		t.Errorf("to8 wrong: %d %d %d", to8(-0.1), to8(0.5), to8(2))
	}
}

func TestCov_rampAt(t *testing.T) {
	st := newTheme()
	fill := st.fill // len 5
	chk := func(pos, span, want int) {
		got := st.rampAt(fill, pos, span)
		if got.GetForeground() != fill[want].GetForeground() {
			t.Errorf("rampAt(pos=%d span=%d) -> %v, want fill[%d]", pos, span, got.GetForeground(), want)
		}
	}
	chk(0, 1, 0)   // span<=1 -> ratio 0
	chk(2, 5, 2)   // middle
	chk(10, 2, 4)  // i>=len -> clamp to last
	chk(-10, 5, 0) // negative pos -> i<0 -> clamp to first
}

func TestCov_hslHexAllArms(t *testing.T) {
	// every hue feeds a different switch arm (int(hp) 0..5)
	for _, h := range []float64{30, 90, 150, 210, 270, 330} {
		hex := hslHex(h, 0.7, 0.5)
		if len(hex) != 7 || hex[0] != '#' {
			t.Errorf("hslHex(%v) = %q, want #rrggbb", h, hex)
		}
	}
	// a negative hue wraps (hp<0 correction) and equals its +360 equivalent
	if hslHex(-30, 1, 0.5) != hslHex(330, 1, 0.5) {
		t.Error("hslHex(-30) should equal hslHex(330)")
	}
	// anchor: pure red
	if got := hslHex(0, 1, 0.5); got != "#ff0000" {
		t.Errorf("hslHex(0,1,0.5) = %q, want #ff0000", got)
	}
}

func TestCov_sparklineAllZero(t *testing.T) {
	// an all-zero series has zero span (and zero floor) -> the span>0 false branch
	if got := sparkline([]float64{0, 0, 0}, 5); got != "▁▁▁" {
		t.Errorf("sparkline(zeros) = %q, want ▁▁▁", got)
	}
	if got := sparkline([]float64{1, 2, 3, 4}, 2); len([]rune(got)) != 2 {
		t.Errorf("sparkline width cap len = %d, want 2", len([]rune(got)))
	}
}

// ============================================================================
// mouse.go fraction mappers
// ============================================================================

func TestCov_fracMappers(t *testing.T) {
	if fracToVol(-0.5) != 0 || fracToVol(0.5) != 50 || fracToVol(1.5) != 100 {
		t.Errorf("fracToVol wrong: %d %d %d", fracToVol(-0.5), fracToVol(0.5), fracToVol(1.5))
	}
	// hfrac: den<1 clamp, and value clamps
	if got := hfrac(rect{x: 0, w: 1}, 0); got != 0 {
		t.Errorf("hfrac den<1 = %v, want 0", got)
	}
	if got := hfrac(rect{x: 0, w: 11}, 5); got != 0.5 {
		t.Errorf("hfrac mid = %v, want 0.5", got)
	}
	if got := hfrac(rect{x: 5, w: 11}, 0); got != 0 {
		t.Errorf("hfrac below = %v, want 0", got)
	}
	if got := hfrac(rect{x: 0, w: 11}, 20); got != 1 {
		t.Errorf("hfrac above = %v, want 1", got)
	}
	// vfrac: top=1, bottom=0, den<1 clamp (a single row is the top -> full), value clamps
	if got := vfrac(rect{y: 0, h: 1}, 0); got != 1 {
		t.Errorf("vfrac den<1 = %v, want 1", got)
	}
	if got := vfrac(rect{y: 0, h: 11}, 0); got != 1 {
		t.Errorf("vfrac top = %v, want 1", got)
	}
	if got := vfrac(rect{y: 0, h: 11}, -5); got != 1 {
		t.Errorf("vfrac above = %v, want 1", got)
	}
	if got := vfrac(rect{y: 0, h: 11}, 20); got != 0 {
		t.Errorf("vfrac below = %v, want 0", got)
	}
}

// ============================================================================
// friendlyError — every mapped case
// ============================================================================

func TestCov_friendlyErrorAllCases(t *testing.T) {
	cases := map[string]string{
		"ssh: Could not resolve hostname x":      "can't find the device — are you on the home network?",
		"getaddrinfo: Name or service not known": "can't find the device — are you on the home network?",
		"No route to host":                       "no route to the device — check the network",
		"network is unreachable":                 "no route to the device — check the network",
		"Connection refused":                     "the device refused the connection",
		"Operation timed out":                    "connection timed out — the device may be off or away",
		"i/o timeout":                            "connection timed out — the device may be off or away",
		"Permission denied (publickey).":         "ssh authentication failed",
		"ssh: some other failure":                "some other failure", // prefix strip
		"a plain message with no markers":        "a plain message with no markers",
	}
	for in, want := range cases {
		if got := friendlyError(in); got != want {
			t.Errorf("friendlyError(%q) = %q, want %q", in, got, want)
		}
	}
}

// ============================================================================
// translate / translateAll — every key Type
// ============================================================================

func TestCov_translateEveryType(t *testing.T) {
	cases := []struct {
		typ  tea.KeyType
		want keyKind
	}{
		{tea.KeyEnter, kEnter}, {tea.KeyEsc, kEsc}, {tea.KeyBackspace, kBackspace},
		{tea.KeyLeft, kLeft}, {tea.KeyRight, kRight}, {tea.KeyUp, kUp}, {tea.KeyDown, kDown},
		{tea.KeyTab, kTab}, {tea.KeyShiftTab, kShiftTab}, {tea.KeySpace, kRune},
		{tea.KeyCtrlA, kOther}, // unmapped
	}
	for _, c := range cases {
		if got := translate(tea.KeyMsg{Type: c.typ}); got.kind != c.want {
			t.Errorf("translate(%v).kind = %d, want %d", c.typ, got.kind, c.want)
		}
	}
	if ev := translate(tea.KeyMsg{Type: tea.KeySpace}); ev.r != ' ' {
		t.Errorf("space rune = %q, want space", ev.r)
	}
	// single-rune KeyRunes
	if ev := translate(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}}); ev.kind != kRune || ev.r != 'm' {
		t.Errorf("single rune = %+v", ev)
	}
	// multi-rune expands 1:1 via translateAll
	evs := translateAll(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a', 'b', 'c'}})
	if len(evs) != 3 || evs[2].r != 'c' {
		t.Errorf("translateAll multi-rune = %+v", evs)
	}
}

// ============================================================================
// key dispatch — rune keys and pane-specific directionals/enter
// ============================================================================

func TestCov_KeyRunes(t *testing.T) {
	m, st, collect := makeModel(t)
	st.SetVol(50)
	collect()

	// space -> toggle (playing fixture -> PAUSE)
	m.key(kr(' '))
	if c := last(collect()); c.Mid != 40 || c.Data != "PAUSE" {
		t.Errorf("space -> %+v, want 40 PAUSE", c)
	}
	m.key(kr('n'))
	if last(collect()).Data != "NEXT" {
		t.Error("n should next")
	}
	m.key(kr('p'))
	if last(collect()).Data != "PREV" {
		t.Error("p should prev")
	}
	st.SetVol(50)
	collect()
	m.key(kr('='))
	if last(collect()).Data != "52" {
		t.Error("= should volup")
	}
	m.key(kr('+'))
	if last(collect()).Data != "54" {
		t.Error("+ should volup")
	}
	m.key(kr('-'))
	if last(collect()).Data != "52" {
		t.Error("- should voldn")
	}
	m.key(kr('_'))
	if last(collect()).Data != "50" {
		t.Error("_ should voldn")
	}
	// e focuses the EQ pane
	m.key(kr('e'))
	if m.pane != paneEQ {
		t.Error("e should focus EQ pane")
	}
	// an unmapped rune is a no-op
	if got := m.key(kr('z')); got != "" {
		t.Errorf("unmapped rune -> %q, want \"\"", got)
	}
	// Q quits too
	if m.key(kr('Q')) != "quit" {
		t.Error("Q should quit")
	}
}

func TestCov_KeyPanes(t *testing.T) {
	m, _, _ := makeModel(t)

	// EQ pane: up/down move the band selection, left/right adjust, enter toggles
	m.pane = paneEQ
	m.eqFocus = 1 // TRE (ranged)
	m.key(ke(kUp))
	if m.eqFocus != 0 {
		t.Errorf("EQ up: eqFocus = %d, want 0", m.eqFocus)
	}
	m.key(ke(kDown))
	if m.eqFocus != 1 {
		t.Errorf("EQ down: eqFocus = %d, want 1", m.eqFocus)
	}
	m.key(ke(kLeft))  // eqAdjust(-1)
	m.key(ke(kRight)) // eqAdjust(+1)
	m.eqFocus = 0     // EQS (toggle)
	before, _ := m.st.EQValue("EQS")
	m.key(ke(kEnter)) // toggle EQS
	after, _ := m.st.EQValue("EQS")
	if before == after {
		t.Error("enter in EQ pane should toggle the focused band")
	}

	// shift+tab also switches panes
	p := m.pane
	m.key(ke(kShiftTab))
	if m.pane == p {
		t.Error("shift+tab should switch panes")
	}

	// now-playing pane: up/down adjust volume
	m.pane = paneNow
	m.st.SetVol(50)
	m.key(ke(kUp))
	if v := m.st.Snap().Vol; v != 52 {
		t.Errorf("now-pane up: vol = %d, want 52", v)
	}
	m.key(ke(kDown))
	if v := m.st.Snap().Vol; v != 50 {
		t.Errorf("now-pane down: vol = %d, want 50", v)
	}
}

// ============================================================================
// Update — every message branch
// ============================================================================

func TestCov_UpdateMessages(t *testing.T) {
	// WindowSizeMsg sets rows/cols
	m, _, _ := makeModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if m.cols != 100 || m.rows != 40 {
		t.Errorf("WindowSizeMsg: %dx%d, want 100x40", m.cols, m.rows)
	}

	// logicMsg advances the marquee and reschedules
	scroll := m.scroll
	if _, cmd := m.Update(logicMsg{}); cmd == nil || m.scroll != scroll+1 {
		t.Error("logicMsg should advance scroll and reschedule")
	}

	// frameMsg with the sonar live while disconnected advances the frame
	d, _, _ := modelWith(protocol.NewState())
	d.sonarLive = true
	f := d.frame
	if _, cmd := d.Update(frameMsg{}); cmd == nil {
		t.Error("frameMsg should reschedule")
	}
	if d.frame != f+1 {
		t.Errorf("sonar frame %d -> %d, want +1", f, d.frame)
	}

	// MouseMsg is dispatched to handleMouse (no panic)
	if _, cmd := m.Update(tea.MouseMsg{X: 1, Y: 1, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}); cmd != nil {
		t.Error("MouseMsg returns no command")
	}

	// Ctrl-C marks interrupted and quits
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil || !m.interrupted {
		t.Error("ctrl-c should set interrupted and quit")
	}
}

// ============================================================================
// controller methods: do RESUME, eqCur, eqToggleFocused no-op
// ============================================================================

func TestCov_doResumeAndUnmute(t *testing.T) {
	// a fresh state is paused (playing == 2), so a toggle RESUMEs (the other arm)
	m, _, collect := modelWith(protocol.NewState())
	m.do("toggle")
	if c := last(collect()); c.Data != "RESUME" {
		t.Errorf("toggle from paused -> %+v, want RESUME", c)
	}

	// unmute from a non-zero premute restores it
	m2, st2, collect2 := makeModel(t)
	st2.SetVol(40)
	m2.do("mute") // vol>0 -> 0
	if last(collect2()).Data != "0" {
		t.Error("mute should silence")
	}
	m2.do("mute") // vol==0, premute 40 -> restore
	if c := last(collect2()); c.Data != "40" {
		t.Errorf("unmute -> %+v, want 40", c)
	}
}

func TestCov_eqCurAndToggleNoop(t *testing.T) {
	m, st, _ := modelWith(protocol.NewState())
	if m.eqCur("MXV") != 0 {
		t.Error("eqCur unknown should be 0")
	}
	st.ApplyTunnel("MXV", 40)
	if m.eqCur("MXV") != 40 {
		t.Error("eqCur known should report the value")
	}
	// eqToggleFocused is a no-op on a ranged control
	m.pane, m.eqFocus = paneEQ, 1 // TRE (ranged)
	v, _ := st.EQValue("TRE")
	m.eqToggleFocused()
	if nv, _ := st.EQValue("TRE"); nv != v {
		t.Error("eqToggleFocused on a ranged band must be a no-op")
	}
}

func TestCov_computeTitle(t *testing.T) {
	// idle -> the device name, with non-printable runes filtered out
	m, _, _ := modelWith(protocol.NewState())
	m.cfg.Name = "ab\x01cd"
	if got := m.computeTitle(m.st.Snap()); got != "abcd" {
		t.Errorf("computeTitle(idle, ctrl char) = %q, want abcd", got)
	}
	// over-long names are capped at MaxTitleLength
	m.cfg.Name = strings.Repeat("x", MaxTitleLength+10)
	if got := m.computeTitle(m.st.Snap()); len([]rune(got)) != MaxTitleLength {
		t.Errorf("computeTitle long name len = %d, want %d", len([]rune(got)), MaxTitleLength)
	}
	// a track title is "♪ Name — Artist"
	mp, _, _ := makeModel(t)
	if got := mp.computeTitle(mp.st.Snap()); !strings.Contains(got, "De Música Ligera") {
		t.Errorf("computeTitle(track) = %q", got)
	}
}

// ============================================================================
// Init / ticks / cellPixelSize
// ============================================================================

func TestCov_InitTicksCellPixel(t *testing.T) {
	m, _, _ := makeModel(t)
	if m.Init() == nil {
		t.Error("Init should return a batched command")
	}
	// the tick commands sleep their interval then yield the right message type
	if _, ok := logicTick()().(logicMsg); !ok {
		t.Error("logicTick cmd should yield a logicMsg")
	}
	if _, ok := frameTick(framePlaying)().(frameMsg); !ok {
		t.Error("frameTick cmd should yield a frameMsg")
	}
	// cellPixelSize: no tty in tests -> (0,0); just exercise it without panicking
	if w, h := cellPixelSize(); w != 0 || h != 0 {
		t.Logf("cellPixelSize returned %dx%d (a real tty?)", w, h)
	}
}

// ============================================================================
// detectKittyGraphics — environment fingerprints
// ============================================================================

func TestCov_detectKittyGraphics(t *testing.T) {
	clear := func(t *testing.T) {
		for _, k := range []string{"TMUX", "TERM", "KITTY_WINDOW_ID", "GHOSTTY_RESOURCES_DIR", "GHOSTTY_BIN_DIR", "TERM_PROGRAM"} {
			t.Setenv(k, "")
		}
	}
	t.Run("tmux_forces_false", func(t *testing.T) {
		clear(t)
		t.Setenv("TMUX", "/tmp/tmux-1/default")
		t.Setenv("KITTY_WINDOW_ID", "1") // present but ignored under tmux
		if detectKittyGraphics() {
			t.Error("under TMUX should be false")
		}
	})
	t.Run("screen_term_false", func(t *testing.T) {
		clear(t)
		t.Setenv("TERM", "screen-256color")
		if detectKittyGraphics() {
			t.Error("screen TERM should be false")
		}
	})
	t.Run("ghostty_bin_dir_true", func(t *testing.T) {
		clear(t)
		t.Setenv("GHOSTTY_BIN_DIR", "/Applications/Ghostty.app")
		if !detectKittyGraphics() {
			t.Error("GHOSTTY_BIN_DIR should be true")
		}
	})
	t.Run("kitty_window_id_true", func(t *testing.T) {
		clear(t)
		t.Setenv("KITTY_WINDOW_ID", "1")
		if !detectKittyGraphics() {
			t.Error("KITTY_WINDOW_ID should be true")
		}
	})
	t.Run("term_program_kitty_true", func(t *testing.T) {
		clear(t)
		t.Setenv("TERM_PROGRAM", "kitty")
		if !detectKittyGraphics() {
			t.Error("TERM_PROGRAM=kitty should be true")
		}
	})
	t.Run("term_xterm_kitty_true", func(t *testing.T) {
		clear(t)
		t.Setenv("TERM", "xterm-kitty")
		if !detectKittyGraphics() {
			t.Error("TERM=xterm-kitty should be true")
		}
	})
	t.Run("plain_xterm_false", func(t *testing.T) {
		clear(t)
		t.Setenv("TERM", "xterm-256color")
		if detectKittyGraphics() {
			t.Error("plain xterm should be false")
		}
	})
}

// ============================================================================
// stack / frameBody — direct composition
// ============================================================================

func TestCov_stack(t *testing.T) {
	if stack(nil, nil, nil, 0) != nil {
		t.Error("stack(h<=0) should be nil")
	}
	out := stack([]string{"H"}, []string{"M"}, []string{"F"}, 3)
	if len(out) != 3 || out[0] != "H" || out[2] != "F" {
		t.Errorf("stack normal = %v", out)
	}
	// middle overflows the region and is trimmed from the bottom
	out = stack([]string{"H"}, []string{"m1", "m2", "m3"}, []string{"F"}, 3)
	if len(out) != 3 || out[0] != "H" || out[2] != "F" || out[1] != "m1" {
		t.Errorf("stack overflow-trim = %v", out)
	}
}

func TestCov_frameBody(t *testing.T) {
	if frameBody(nil, nil, 0, false) != nil {
		t.Error("frameBody(h<=0) should be nil")
	}
	// tail >= h: only the last h tail lines survive
	out := frameBody([]string{"c"}, []string{"t1", "t2", "t3"}, 2, false)
	if len(out) != 2 || out[0] != "t2" || out[1] != "t3" {
		t.Errorf("frameBody tail>=h = %v", out)
	}
	// content overflows the room above the tail -> trimmed from the bottom
	out = frameBody([]string{"c1", "c2", "c3"}, []string{"F"}, 3, false)
	if len(out) != 3 || out[0] != "c1" || out[2] != "F" {
		t.Errorf("frameBody content-trim = %v", out)
	}
	// centred content
	out = frameBody([]string{"c"}, []string{"F"}, 4, true)
	if len(out) != 4 || out[3] != "F" || out[1] != "c" {
		t.Errorf("frameBody centred = %v", out)
	}
}

// ============================================================================
// renderMini — error / track / paused / idle / connecting
// ============================================================================

func TestCov_renderMini(t *testing.T) {
	// fatal error
	st := protocol.NewState()
	st.SetFatal("Permission denied (publickey).")
	m, _, _ := modelWith(st)
	m.sty = newTheme()
	m.cols = 50
	if out := stripANSI(m.renderMini(m.st.Snap())); !strings.Contains(out, "ssh authentication failed") {
		t.Errorf("mini fatal = %q", out)
	}

	// playing track
	mp, _, _ := makeModel(t)
	mp.sty = newTheme()
	mp.cols = 56
	if out := stripANSI(mp.renderMini(mp.st.Snap())); !strings.Contains(out, "De Música Ligera") {
		t.Errorf("mini track = %q", out)
	}
	// paused track -> the pause glyph
	mp.st.ToggleOptimistic()
	if out := mp.renderMini(mp.st.Snap()); !strings.Contains(out, GL["pause"]) {
		t.Errorf("mini paused should carry the pause glyph: %q", stripANSI(out))
	}

	// connected, nothing playing
	hb := protocol.NewState()
	applyFixtureRecords(hb, "heartbeat_record.txt")
	mh, _, _ := modelWith(hb)
	mh.sty = newTheme()
	mh.cols = 56
	if out := stripANSI(mh.renderMini(mh.st.Snap())); !strings.Contains(out, "nothing playing") {
		t.Errorf("mini connected-idle = %q", out)
	}

	// disconnected -> connecting
	md, _, _ := modelWith(protocol.NewState())
	md.sty = newTheme()
	md.cols = 56
	if out := stripANSI(md.renderMini(md.st.Snap())); !strings.Contains(out, "connecting to LP10") {
		t.Errorf("mini connecting = %q", out)
	}
}

// ============================================================================
// render helpers: sourceStyle, fullSourceLine, seekRow idle, controlsRow,
// dividerRow, metaLines idle, footerRow EQ hint
// ============================================================================

func TestCov_sourceStyle(t *testing.T) {
	st := newTheme()
	cases := map[string]string{
		"Spotify":   "#1db954",
		"TIDAL":     "#4fd4d4",
		"AirPlay":   "#cfd6df",
		"Bluetooth": "#4a90d9",
	}
	for name, hex := range cases {
		if got := sourceStyle(st, name).GetForeground(); got != lipgloss.Color(hex) {
			t.Errorf("sourceStyle(%s) fg = %v, want %s", name, got, hex)
		}
	}
	// unknown falls back to the theme accent
	if sourceStyle(st, "Whatever").GetForeground() != st.sAcc.GetForeground() {
		t.Error("unknown source should fall back to the accent")
	}
}

func TestCov_fullSourceLine(t *testing.T) {
	m, _, _ := makeModel(t)
	m.sty = newTheme()
	s := m.st.Snap()
	// fits -> the brand-tinted source line
	if got := stripANSI(m.fullSourceLine(s, 60)); !strings.Contains(got, "Spotify") {
		t.Errorf("fullSourceLine wide = %q", got)
	}
	// too narrow -> a plain dim clip that still respects the width contract
	if got := m.fullSourceLine(s, 5); DispW(stripANSI(got)) > 5 {
		t.Errorf("fullSourceLine narrow width = %d, want <= 5", DispW(stripANSI(got)))
	}
	// nil track -> ""
	if got := m.fullSourceLine(protocol.Snapshot{}, 60); got != "" {
		t.Errorf("fullSourceLine(nil) = %q, want empty", got)
	}
}

func TestCov_seekRowIdleAndControlsRow(t *testing.T) {
	// idle seek row (nil track) hits the quiet-marker default branch and fills W
	mi, _, _ := modelWith(protocol.NewState())
	mi.sty = newTheme()
	if got := DispW(stripANSI(mi.seekRow(mi.st.Snap(), 60))); got != 60 {
		t.Errorf("idle seekRow width = %d, want 60", got)
	}

	m, st, _ := makeModel(t)
	m.sty = newTheme()
	st.SetVol(50)
	// withVol == false returns just the transport cluster (no volume)
	noVol := stripANSI(m.controlsRow(m.st.Snap(), time.Now(), 80, false))
	if strings.Contains(noVol, "vol") || strings.Contains(noVol, "%") {
		t.Errorf("controlsRow(withVol=false) should omit volume: %q", noVol)
	}
	// muted + withVol shows the MUTED badge
	m.do("mute")
	muted := stripANSI(m.controlsRow(m.st.Snap(), time.Now(), 80, true))
	if !strings.Contains(muted, "MUTED") {
		t.Errorf("controlsRow muted = %q", muted)
	}
}

func TestCov_dividerAndMetaIdleAndFooter(t *testing.T) {
	m, _, _ := makeModel(t)
	m.sty = newTheme()
	// a label wider than the row clamps the rule to 0 without panicking
	if got := m.dividerRow("a-very-long-heading", 2); got == "" {
		t.Error("dividerRow with a tiny W should still render")
	}

	// metaLines while disconnected with an error shows the friendly reason
	st := protocol.NewState()
	st.Note("ssh: Could not resolve hostname lp10.local")
	md, _, _ := modelWith(st)
	md.sty = newTheme()
	lines := md.metaLines(md.st.Snap(), 50)
	joined := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "connecting to LP10") || !strings.Contains(joined, "can't find the device") {
		t.Errorf("metaLines disconnected+error = %q", joined)
	}

	// footer EQ-pane hint
	m.pane = paneEQ
	if got := stripANSI(m.footerRow(80)); !strings.Contains(got, "pick") || !strings.Contains(got, "adjust") {
		t.Errorf("footer EQ hint = %q", got)
	}
}

// ============================================================================
// eqSliderRow — toggle on/off, ranged warm/cool/accent knobs, unknown "—"
// ============================================================================

func TestCov_eqSliderRow(t *testing.T) {
	m, _, _ := modelWith(protocol.NewState())
	m.sty = newTheme()
	const w = 60
	// toggle ON (EQS == Specs[1]) and OFF
	if got := stripANSI(m.eqSliderRow(1, map[string]int{"EQS": 1}, false, w)); !strings.Contains(got, "on") {
		t.Errorf("toggle on = %q", got)
	}
	if got := stripANSI(m.eqSliderRow(1, map[string]int{"EQS": 0}, false, w)); !strings.Contains(got, "off") {
		t.Errorf("toggle off = %q", got)
	}
	// ranged unknown value -> "—"
	if got := stripANSI(m.eqSliderRow(0, map[string]int{}, false, w)); !strings.Contains(got, "—") {
		t.Errorf("ranged unknown = %q", got)
	}
	// ranged tone +/- and a non-negative-min ranged (MXV) accent knob, focused
	if got := stripANSI(m.eqSliderRow(2, map[string]int{"BAS": 5}, true, w)); !strings.Contains(got, "+5") {
		t.Errorf("ranged +5 = %q", got)
	}
	if got := stripANSI(m.eqSliderRow(2, map[string]int{"BAS": -5}, true, w)); !strings.Contains(got, "-5") {
		t.Errorf("ranged -5 = %q", got)
	}
	if got := stripANSI(m.eqSliderRow(0, map[string]int{"MXV": 50}, true, w)); !strings.Contains(got, "50") {
		t.Errorf("ranged MXV = %q", got)
	}
	// trackW < 1 (very narrow) still renders without panicking
	_ = m.eqSliderRow(2, map[string]int{"BAS": 0}, false, 5)

	// an out-of-range device echo drives the knob position past the track ends,
	// exercising the defensive knobPos clamps (above the max, then below the min)
	if got := stripANSI(m.eqSliderRow(2, map[string]int{"BAS": 1000}, false, w)); DispW(got) != w {
		t.Errorf("over-range knob row width = %d, want %d", DispW(got), w)
	}
	if got := stripANSI(m.eqSliderRow(2, map[string]int{"BAS": -1000}, false, w)); DispW(got) != w {
		t.Errorf("under-range knob row width = %d, want %d", DispW(got), w)
	}

	// the warm (boost) and cool (cut) focused knobs emit different styling
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(old)
	warm := m.eqSliderRow(2, map[string]int{"BAS": 5}, true, w)
	cool := m.eqSliderRow(2, map[string]int{"BAS": -5}, true, w)
	if warm == cool {
		t.Error("a boosted (warm) and cut (cool) knob should differ in styling")
	}
}

// ============================================================================
// eqReadout — empty and a full map
// ============================================================================

func TestCov_eqReadout(t *testing.T) {
	m, _, _ := modelWith(protocol.NewState())
	m.sty = newTheme()
	if got := stripANSI(m.eqReadout(map[string]int{})); got != "—" {
		t.Errorf("eqReadout(empty) = %q, want —", got)
	}
	full := map[string]int{"EQS": 1, "TRE": 3, "MID": -2, "BAS": 5, "VBS": 1, "VBI": 40, "MXV": 100}
	got := stripANSI(m.eqReadout(full))
	for _, want := range []string{"EQ on", "T +3", "M -2", "B +5", "Sub on 40", "Max Vol 100"} {
		if !strings.Contains(got, want) {
			t.Errorf("eqReadout missing %q in %q", want, got)
		}
	}
	// the EQS-off and VBS-off arms
	off := stripANSI(m.eqReadout(map[string]int{"EQS": 0, "VBS": 0}))
	if !strings.Contains(off, "EQ off") || !strings.Contains(off, "Sub off") {
		t.Errorf("eqReadout off arms = %q", off)
	}
}

// ============================================================================
// clipStyled — fits and overflows
// ============================================================================

func TestCov_clipStyled(t *testing.T) {
	m, _, _ := modelWith(protocol.NewState())
	m.sty = newTheme()
	if got := m.clipStyled("abc", 10); got != "abc" {
		t.Errorf("clipStyled fits = %q", got)
	}
	if got := m.clipStyled("abcdefgh", 4); stripANSI(got) != "abc"+GL["ell"] {
		t.Errorf("clipStyled overflow = %q", stripANSI(got))
	}
}

// ============================================================================
// renderDashboard — compact path and error lines (full + compact)
// ============================================================================

func TestCov_renderDashboardCompactAndErrors(t *testing.T) {
	// compact layout: cols between MiniCols and FullCols
	m, _, _ := makeModel(t)
	m.rows, m.cols = 20, 64
	out := clean(m.View())
	if !strings.Contains(out, "equalizer") {
		t.Errorf("compact dashboard should still show the EQ summary header")
	}

	// a connected error paints the red error line in the compact tail
	me, st, _ := makeModel(t)
	st.Note("Connection refused")
	me.rows, me.cols = 20, 64
	if !strings.Contains(clean(me.View()), "the device refused the connection") {
		t.Error("compact errLine should show the friendly reason")
	}

	// and in the full layout
	mf, st2, _ := makeModel(t)
	st2.Note("Connection refused")
	mf.rows, mf.cols = 40, 120
	if !strings.Contains(clean(mf.View()), "the device refused the connection") {
		t.Error("full errLine should show the friendly reason")
	}
}

// ============================================================================
// art: noteBox, ghostCover (kitty), boxArt (ambient frame)
// ============================================================================

func TestCov_noteBox(t *testing.T) {
	m := artModel(t)
	// normal motif (room for the 5x3 note motif)
	normal := strings.Join(m.noteBox(10, 6), "\n")
	if !strings.Contains(normal, "●") {
		t.Errorf("noteBox normal should draw the note motif: %q", normal)
	}
	// too small for the motif -> a single centred ♪
	small := strings.Join(m.noteBox(3, 2), "\n")
	if !strings.Contains(small, GL["note"]) {
		t.Errorf("noteBox small should fall back to a single note: %q", small)
	}
	// width 0 -> blank lines, no panic
	if got := m.noteBox(0, 3); len(got) != 3 {
		t.Errorf("noteBox(0,3) len = %d, want 3", len(got))
	}
}

func TestCov_ghostCoverKitty(t *testing.T) {
	m := artModel(t)
	m.cfg.ArtMode = "auto"
	m.sty.trueColor = true
	m.sty.kittyGraphics = true
	s := protocol.Snapshot{Connected: true,
		LastArt: fillImg(40, 40, color.RGBA{200, 60, 40, 255}), LastCoverURL: "http://x/last"}
	lines := m.ghostCover(s, 16, 8)
	if len(lines) != 8 {
		t.Fatalf("kitty ghost should be 8 lines, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "\x1b_G") {
		t.Error("kitty ghost first line should carry the transmit escape")
	}
	// no remembered cover -> nil (caller falls back to the note motif)
	if g := m.ghostCover(protocol.Snapshot{Connected: true}, 16, 8); g != nil {
		t.Error("ghostCover with no LastArt should be nil")
	}
}

func TestCov_boxArtAmbientFrame(t *testing.T) {
	m := artModel(t)
	m.amb = m.sty.tint(color.RGBA{210, 30, 30, 255}) // a red ambient tint lights the frame
	framed := m.boxArt([]string{"abcd"}, 4)
	if len(framed) != 3 {
		t.Fatalf("boxArt should add top/bottom rows, got %d", len(framed))
	}
	if !strings.Contains(framed[0], GL["tl"]) || !strings.Contains(framed[2], GL["bl"]) {
		t.Error("boxArt missing frame corners")
	}
}

// ============================================================================
// diagCard direct (defensive negative-bar) + latencyRow
// ============================================================================

func TestCov_diagCardNarrow(t *testing.T) {
	m, _, _ := modelWith(protocol.NewState())
	m.sty = newTheme()
	// a title much wider than the card forces the negative-width bar branch to 0
	out := m.diagCard(strings.Repeat("x", 40), []string{"row"}, 16)
	if len(out) != 3 {
		t.Errorf("diagCard should be header+row+footer, got %d lines", len(out))
	}
}

// ============================================================================
// renderDiag — Wi-Fi stacked and Wi-Fi cards, plus the short-pane trim
// ============================================================================

// wifiDev returns a synthetic @@i device section for a Wi-Fi link at the given MHz.
func wifiDev(freq string) string {
	return "@@i\nnet=wifi\niface=wlan0\nip=192.168.1.50\nmac=aa:bb:cc:dd:ee:01\n" +
		"gw=192.168.1.1\nssid=HomeNet\nfreq=" + freq + "\nrate=433\n" +
		"build=2025-12-24\napp=312\nplatform=LS8\ndata=1258291 7340032\ndns=192.168.1.1\n@@E\n"
}

// two Wi-Fi @@s heartbeats (signal/noise/linkq + audio chain + pings); the second
// bumps the byte counters so the throughput rates compute (RatesOK).
const (
	wifiS1 = "@@s\n12350 0.5 0.4 0.3 138500 221064 2 AR241CE_9243.16 Linux-5.15.137 52400 1000 500 -55 50 2.1 0.9 22.4 RUNNING 4834 44100 S16_LE 2 22050 1200000 2/237 -90\n@@E\n"
	wifiS2 = "@@s\n12351 0.5 0.4 0.3 138500 221064 2 AR241CE_9243.16 Linux-5.15.137 52400 2000 1500 -55 50 2.2 1.0 23.0 RUNNING 4834 44100 S16_LE 2 22050 1200000 2/237 -90\n@@E\n"
)

func applyRaw(st *protocol.State, raw string) {
	lines := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
	for rec := range protocol.IterRecords(feeder(lines)) {
		protocol.ApplyRecord(st, rec)
	}
}

func TestCov_renderDiagWifiStacked(t *testing.T) {
	st := protocol.NewState()
	applyRaw(st, wifiDev("5180")) // 5 GHz, channel 36
	applyRaw(st, wifiS1)
	applyRaw(st, wifiS2)
	m, _, _ := modelWith(st)
	m.rows, m.cols = 44, 99 // W = 93 < diagCardsMinW -> stacked
	m.diag = true
	out := clean(m.View())
	for _, want := range []string{"wi-fi", "HomeNet", "ch 36", "5 GHz", "signal", "latency", "you", "gw"} {
		if !strings.Contains(out, want) {
			t.Errorf("wifi stacked diag missing %q", want)
		}
	}
}

func TestCov_renderDiagWifiCards(t *testing.T) {
	st := protocol.NewState()
	applyRaw(st, wifiDev("2437")) // 2.4 GHz, channel 6
	applyRaw(st, wifiS1)
	applyRaw(st, wifiS2)
	m, _, _ := modelWith(st)
	m.rows, m.cols = 44, 120 // W = 114 >= diagCardsMinW -> cards
	m.diag = true
	out := clean(m.View())
	for _, want := range []string{"╭─ network", "wi-fi", "ch 6", "2.4 GHz", "snr", "dns", "rate", "433 Mbit/s"} {
		if !strings.Contains(out, want) {
			t.Errorf("wifi cards diag missing %q", want)
		}
	}
}

func TestCov_renderDiagStackedShortPane(t *testing.T) {
	st := protocol.NewState()
	applyRaw(st, wifiDev("5180"))
	applyRaw(st, wifiS1)
	applyRaw(st, wifiS2)
	m, _, _ := modelWith(st)
	m.rows, m.cols = 18, 99 // too short: the read-out must be trimmed with a hint
	m.diag = true
	out := clean(m.View())
	if !strings.Contains(out, "resize for more") {
		t.Errorf("a short diag pane should trim with a 'resize for more' hint:\n%s", out)
	}
}

// ============================================================================
// Second pass: remaining small/defensive branches.
// ============================================================================

func TestCov_nbSendDropOldest(t *testing.T) {
	// a full buffer drops the oldest queued item and retries (the non-blocking path)
	ch := make(chan int, 1)
	nbSend(ch, 1)
	nbSend(ch, 2) // full -> drop 1, enqueue 2
	if got := <-ch; got != 2 {
		t.Errorf("nbSend drop-oldest = %d, want 2", got)
	}
}

func TestCov_UpdateUnknownAndViewZero(t *testing.T) {
	m, _, _ := makeModel(t)
	// an unrecognized message type falls through to (m, nil)
	if _, cmd := m.Update(struct{ unknownMsg int }{}); cmd != nil {
		t.Error("unknown msg should return no command")
	}
	// a 0-sized window renders nothing
	m.rows, m.cols = 0, 80
	if m.View() != "" {
		t.Error("View at 0 rows should be empty")
	}
}

func TestCov_themeDegenerate(t *testing.T) {
	st := newTheme()
	if st.sonar(0, 5, 0) != nil {
		t.Error("sonar(w<=0) should be nil")
	}
	// a 1x1 box clamps the scope radius (fitR<1)
	if got := st.sonar(1, 1, 0); len(got) != 1 {
		t.Errorf("sonar(1,1) len = %d, want 1", len(got))
	}
	// lineMeterPen with no cells is empty; frac 0 puts the head at cell 0
	if st.lineMeterPen(0.5, 0, st.fill, st.head) != "" {
		t.Error("lineMeterPen(cells<=0) should be empty")
	}
	if got := stripANSI(st.lineMeter(0, 10)); !strings.Contains(got, "●") {
		t.Errorf("lineMeter(frac 0) should still draw the head: %q", got)
	}
}

func TestCov_handleMouseReleaseNoop(t *testing.T) {
	m, _, collect := makeModel(t)
	render(m, 40, 120)
	// a release (neither press, drag, nor wheel) is ignored
	m.handleMouse(tea.MouseMsg{X: 10, Y: 10, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	if c := collect(); len(c) != 0 {
		t.Errorf("a mouse release should be a no-op, got %+v", c)
	}
}

func TestCov_marqueeZeroWidth(t *testing.T) {
	m, _, _ := makeModel(t)
	if m.marquee("anything", 0) != "" {
		t.Error("marquee(w<=0) should be empty")
	}
}

func TestCov_transportLayoutNarrow(t *testing.T) {
	// too narrow for inter-button gaps: falls back to a solid cluster (gap 0)
	pad, widths, gap := transportLayout(2)
	if gap != 0 || len(widths) != len(actions) {
		t.Errorf("transportLayout(2) = pad %d widths %v gap %d, want gap 0", pad, widths, gap)
	}
	sum := 0
	for _, w := range widths {
		sum += w
	}
	if sum != 2 {
		t.Errorf("narrow transport widths sum %d, want 2", sum)
	}
}

func TestCov_stackRegionClamp(t *testing.T) {
	// top + bottom exceed h: the region clamps to 0, tail still pins to the bottom
	out := stack([]string{"a", "b"}, []string{"m"}, []string{"y", "z"}, 3)
	if len(out) != 3 || out[2] != "z" {
		t.Errorf("stack region<0 = %v", out)
	}
}

func TestCov_sparklineDecreasing(t *testing.T) {
	// a falling series exercises the running-min (v<lo) update
	got := []rune(sparkline([]float64{9, 5, 1}, 10))
	if got[0] != '█' || got[len(got)-1] != '▁' {
		t.Errorf("falling sparkline = %q, want high…low", string(got))
	}
}

func TestCov_metaLinesVariants(t *testing.T) {
	m, _, _ := makeModel(t)
	m.sty = newTheme()

	// connected & idle -> the "start something" hint
	hb := protocol.NewState()
	applyFixtureRecords(hb, "heartbeat_record.txt")
	mh, _, _ := modelWith(hb)
	mh.sty = newTheme()
	if got := stripANSI(strings.Join(mh.metaLines(mh.st.Snap(), 50), "\n")); !strings.Contains(got, "start something") {
		t.Errorf("connected idle metaLines = %q", got)
	}

	// empty title -> "—", artist + album joined on the second line
	s1 := protocol.Snapshot{Track: protocol.Track{"TrackName": "", "Artist": "A", "Album": "Al"}}
	l1 := stripANSI(strings.Join(m.metaLines(s1, 40), "\n"))
	if !strings.Contains(l1, "—") || !strings.Contains(l1, "A · Al") {
		t.Errorf("metaLines empty-title = %q", l1)
	}

	// no artist but an album -> the album-search link fallback path
	s2 := protocol.Snapshot{Track: protocol.Track{"TrackName": "T", "Artist": "", "Album": "OnlyAlbum"}}
	l2 := stripANSI(strings.Join(m.metaLines(s2, 40), "\n"))
	if !strings.Contains(l2, "OnlyAlbum") {
		t.Errorf("metaLines album-only = %q", l2)
	}
}

func TestCov_fullMetaVariants(t *testing.T) {
	m, _, _ := makeModel(t)
	m.sty = newTheme()
	// empty title -> "—", and with no artist/album there's a single line
	s := protocol.Snapshot{Track: protocol.Track{"TrackName": ""}}
	out := m.fullMeta(s, 40)
	if len(out) != 1 || !strings.Contains(stripANSI(out[0]), "—") {
		t.Errorf("fullMeta empty-title = %v", out)
	}
}

func TestCov_fullSourceLineVariants(t *testing.T) {
	m, _, _ := makeModel(t)
	m.sty = newTheme()
	// a track with no source/format -> ""
	if got := m.fullSourceLine(protocol.Snapshot{Track: protocol.Track{"TrackName": "x"}}, 60); got != "" {
		t.Errorf("fullSourceLine no-format = %q, want empty", got)
	}
	// a channel count appends "· N ch"
	s := protocol.Snapshot{Track: protocol.Track{"PlayUrl": "spotify:track:x", "Mime": "audio/ogg", "SampleRate": 44100, "ChannelCount": 2}}
	if got := stripANSI(m.fullSourceLine(s, 60)); !strings.Contains(got, "2 ch") {
		t.Errorf("fullSourceLine channel count = %q", got)
	}
}

func TestCov_seekRowNarrowAndTint(t *testing.T) {
	m, _, _ := makeModel(t)
	m.sty = newTheme()
	// a very narrow column clamps the meter to a single cell without panicking
	// (the fixed status/time fields mean it can't honour W below their overhead)
	if got := stripANSI(m.seekRow(m.st.Snap(), 6)); got == "" {
		t.Error("narrow seekRow should still render")
	}
	// an ambient tint recolours the seek bar (the m.amb != nil branch)
	m.amb = m.sty.tint(color.RGBA{210, 30, 30, 255})
	_ = m.seekRow(m.st.Snap(), 60)
}

func TestCov_headerRowReconnectAndNarrow(t *testing.T) {
	st := protocol.NewState()
	st.StartProc(&protocol.Proc{})
	st.StartProc(&protocol.Proc{}) // attempts -> 2, still disconnected
	m, _, _ := modelWith(st)
	m.sty = newTheme()
	if m.st.Snap().Attempts <= 1 {
		t.Skip("attempts not bumped by StartProc on this build")
	}
	if got := stripANSI(m.headerRow(m.st.Snap(), time.Now(), 80, false)); !strings.Contains(got, "reconnecting") {
		t.Errorf("disconnected header should read reconnecting: %q", got)
	}
	// a tiny width drives the device-name budget below its floor (nameMax clamp)
	_ = m.headerRow(m.st.Snap(), time.Now(), 12, false)
}

func TestCov_eqSummaryArms(t *testing.T) {
	// unknown values -> "code —" parts
	mu, _, _ := modelWith(protocol.NewState())
	mu.sty = newTheme()
	if got := stripANSI(mu.eqSummary(120)); !strings.Contains(got, "—") {
		t.Errorf("eqSummary unknown = %q", got)
	}
	// a toggle that's on -> "on"
	on := protocol.NewState()
	on.PreloadEQ(map[string]int{"EQS": 1})
	mo, _, _ := modelWith(on)
	mo.sty = newTheme()
	if got := stripANSI(mo.eqSummary(120)); !strings.Contains(got, "EQ on") {
		t.Errorf("eqSummary toggle-on = %q", got)
	}
}

func TestCov_eqSliderRowTogglePadClamp(t *testing.T) {
	m, _, _ := modelWith(protocol.NewState())
	m.sty = newTheme()
	// a very narrow toggle row drives the content-pad below zero (clamped)
	_ = m.eqSliderRow(1, map[string]int{"EQS": 1}, false, 9)
}

func TestCov_diagCardTinyWidth(t *testing.T) {
	m, _, _ := modelWith(protocol.NewState())
	m.sty = newTheme()
	// w below the frame overhead clamps the inner width to 1
	if out := m.diagCard("t", []string{"r"}, 3); len(out) != 3 {
		t.Errorf("diagCard tiny width lines = %d, want 3", len(out))
	}
}

// ============================================================================
// Rich diagnostics scenarios — Wi-Fi cards/stacked arms: muted volume, warn/red
// health bands, buffer fill bands, latency spike, SNR vs link-quality detail,
// the channel count, the discovered tag, and the "LUCI silent" header.
// ============================================================================

// sBase is a 26-field @@s heartbeat (see protocol @@s field order). sRec clones
// it and overrides the given field indices, so each scenario tweaks just what it
// needs (1=load1m, 6=ncpu, 9=tempmC, 10/11=rx/tx, 12/13=signal/linkq, 14=youPing,
// 18=bufAvail, 22=bufSize, 25=noise).
var sBase = []string{
	"12350", "0.5", "0.4", "0.3", "138500", "221064", "2", "AR241CE_9243.16",
	"Linux-5.15.137", "52400", "1000", "500", "-55", "50", "2.1", "0.9", "22.4",
	"RUNNING", "4834", "44100", "S16_LE", "2", "22050", "1200000", "2/237", "-90",
}

func sRec(over map[int]string) string {
	f := append([]string(nil), sBase...)
	for i, v := range over {
		f[i] = v
	}
	return "@@s\n" + strings.Join(f, " ") + "\n@@E\n"
}

// richDiag builds a connected, muted, Wi-Fi state with warn/red metrics, a
// channel-count track, a tunnel-live flag, and a latency spike. over patches the
// final @@s sample (e.g. to drive a different buffer-fill band).
func richDiag(t *testing.T, freq string, over map[int]string) (*model, *protocol.State) {
	t.Helper()
	st := protocol.NewState()
	st.StartProc(&protocol.Proc{}) // attempts -> 1 (singular "attempt"); records flow in below
	st.SetEQConnected(true)        // control tunnel "live"
	st.PreloadEQ(map[string]int{"EQS": 1, "TRE": 3, "MID": -2, "BAS": 5, "VBS": 1, "VBI": 40, "MXV": 100})
	st.Preload(protocol.Track{"TrackName": "X", "Mime": "audio/flac", "SampleRate": 44100, "ChannelCount": 2}, 1000, 0)
	applyRaw(st, wifiDev(freq))
	// a data record marks the link connected; vol 0 + connected => muted
	applyRaw(st, "@@p\nMID-Read:49 Data:1000 Length:4\n@@v\nMID-Read:64 Data:0 Length:1\n@@E\n")
	st.SetVol(0)
	// warn cpu (load 1.4 / 2 cores = 70%), red temp (80 °C), plus a rising ping spike
	applyRaw(st, sRec(map[int]string{1: "1.4", 9: "80000"}))
	applyRaw(st, sRec(map[int]string{1: "1.4", 9: "80000", 10: "2000", 11: "1500"}))
	last := map[int]string{1: "1.4", 9: "80000", 10: "3000", 11: "2500", 14: "50"}
	for k, v := range over {
		last[k] = v
	}
	applyRaw(st, sRec(last))
	m, _, _ := modelWith(st)
	m.cfg.Discovered = true // the · mDNS host tag
	return m, st
}

func TestCov_diagRichCards(t *testing.T) {
	m, _ := richDiag(t, "5180", nil) // 5 GHz, buffer warn (fill ~0.3), SNR via noise
	m.rows, m.cols = 44, 120
	m.diag = true
	out := clean(m.View())
	for _, want := range []string{"5 GHz", "ch 36", "snr", "MUTED", "2 ch", "mDNS", "live", "╭─ latency"} {
		if !strings.Contains(out, want) {
			t.Errorf("rich cards diag missing %q", want)
		}
	}
}

func TestCov_diagRichStacked(t *testing.T) {
	m, _ := richDiag(t, "5180", nil)
	m.rows, m.cols = 44, 99
	m.diag = true
	out := clean(m.View())
	for _, want := range []string{"5 GHz", "muted", "link 50/70", "mDNS"} {
		if !strings.Contains(out, want) {
			t.Errorf("rich stacked diag missing %q", want)
		}
	}
}

func TestCov_diagCardsBufferRedAndLinkQ(t *testing.T) {
	// bufAvail > bufSize -> negative fill clamped to 0 -> the red buffer arm; and an
	// absent noise floor falls back to the link-quality SNR detail; ncpu 0 -> clamp.
	m, _ := richDiag(t, "5180", map[int]string{18: "30000", 25: "-", 6: "0"})
	m.rows, m.cols = 44, 120
	m.diag = true
	if out := clean(m.View()); !strings.Contains(out, "link 50/70") {
		t.Errorf("buffer-red cards should fall back to link quality: missing in\n%s", out)
	}
}

func TestCov_diagCardsSilentHeader(t *testing.T) {
	m, st := richDiag(t, "5180", nil)
	m.sty = newTheme()
	m.rows = 44
	_, dData, _, _, _ := st.DiagView()
	if dData.IsZero() {
		t.Fatal("setup: expected a last-data stamp")
	}
	// a now well past the watchdog's SilentAfter flags "LUCI silent" in the header
	out := stripANSI(m.renderDiagCards(st.Snap(), dData.Add(workers.SilentAfter+time.Second), 114))
	if !strings.Contains(out, "LUCI silent") {
		t.Errorf("stale cards header should read LUCI silent: %q", firstLine(out))
	}
}

// ============================================================================
// Final pass: full-layout geometry clamps, degenerate hit-zones, and the last
// few diagnostics arms.
// ============================================================================

// TestCov_renderDashboardGeometry forces the full player's cover-sizing clamps by
// calling renderDashboard with full=true at a tiny width / short height and a
// known cell-pixel size, so coverH<6, the maxW reservation, coverW<8, and the
// measured-cell aspect branch all fire in one paint.
func TestCov_renderDashboardGeometry(t *testing.T) {
	m, _, _ := makeModel(t)
	m.sty = newTheme()
	m.cellW, m.cellH = 8, 16 // a real measured cell aspect (the m.cellW>0 branch)
	m.rows = 18              // short inner region -> coverH floors to 6
	out := m.renderDashboard(m.st.Snap(), time.Now(), 40, true)
	if out == "" {
		t.Error("renderDashboard should produce a body")
	}
}

// TestCov_recordFullZonesDegenerate drives the hit-zone math past its guards: a
// tail taller than the region (region<0, middleLen clamp), a volume bar taller
// than the visible region, and a track width below 1.
func TestCov_recordFullZonesDegenerate(t *testing.T) {
	m, _, _ := makeModel(t)
	m.recordFullZones(10, 5, 20, 5, 100, 10, 10)
	// it records EQ zones even in the degenerate case (trackW clamped to 1)
	if len(m.mzEQ) != len(eqOrder) {
		t.Errorf("recorded %d EQ zones, want %d", len(m.mzEQ), len(eqOrder))
	}
}

func TestCov_diagStackedNcpuZero(t *testing.T) {
	// ncpu 0 -> the nc<1 clamp on the stacked resources gauge
	m, _ := richDiag(t, "5180", map[int]string{6: "0"})
	m.rows, m.cols = 44, 99
	m.diag = true
	if clean(m.View()) == "" {
		t.Error("stacked diag with ncpu 0 should still render")
	}
}

func TestCov_diagCardsBufferWarn(t *testing.T) {
	// bufAvail 15435 of 22050 -> fill ~0.30 -> the stWarn buffer arm
	m, _ := richDiag(t, "5180", map[int]string{18: "15435"})
	m.rows, m.cols = 44, 120
	m.diag = true
	if !strings.Contains(clean(m.View()), "buffer") {
		t.Error("buffer-warn cards should still draw the buffer gauge")
	}
}

func TestCov_diagCardsMissingPing(t *testing.T) {
	// only the laptop ping reports; the gateway and internet targets are skipped
	// (the !ps.OK continue) in the latency card.
	st := protocol.NewState()
	applyRaw(st, wifiDev("5180"))
	applyRaw(st, "@@p\nMID-Read:49 Data:1000 Length:4\n@@v\nMID-Read:64 Data:44 Length:2\n@@E\n")
	applyRaw(st, sRec(map[int]string{15: "-", 16: "-"})) // gw + net pings absent
	m, _, _ := modelWith(st)
	m.rows, m.cols = 44, 120
	m.diag = true
	out := clean(m.View())
	if !strings.Contains(out, "╭─ latency") || !strings.Contains(out, "you") {
		t.Errorf("latency card should still show the 'you' row: %q", out)
	}
}

func TestCov_diagCardsDeviceError(t *testing.T) {
	m, st := richDiag(t, "5180", nil)
	st.Note("Connection refused") // a device error pins to the card tail (derr != "")
	m.rows, m.cols = 44, 120
	m.diag = true
	if !strings.Contains(clean(m.View()), "the device refused the connection") {
		t.Error("a device error should show its friendly reason in the cards tail")
	}
}

func TestCov_latencyRowWideFields(t *testing.T) {
	m, _, _ := modelWith(protocol.NewState())
	m.sty = newTheme()
	// a long name and wide numeric fields make the inner pad/rpad no-ops (return s)
	ps := protocol.PingStat{Avg: 12345, Jitter: 6789, Peak: 99999, Series: []float64{1, 2}, OK: true}
	row := stripANSI(m.latencyRow("verylongname", ps, 8))
	if !strings.Contains(row, "verylongname") || !strings.Contains(row, "12345") {
		t.Errorf("wide latency row = %q", row)
	}
}

// ============================================================================
// Kitty encode-failure degrade paths. KittyImage returns empty when the cell
// width exceeds the placeholder diacritic table (297), which is the reachable
// trigger for artColumn / ghostCover to degrade (half-block under truecolor,
// else the motif / nil).
// ============================================================================

func TestCov_artColumnKittyDegrade(t *testing.T) {
	img := fillImg(40, 40, color.RGBA{20, 180, 90, 255})
	s := protocol.Snapshot{Track: protocol.Track{"TrackName": "x"}, CoverURL: "u", Art: img}

	// truecolor: a failed kitty encode degrades to the half-block raster
	m := artModel(t)
	m.cfg.ArtMode = "kitty"
	m.sty.kittyGraphics = true
	m.sty.trueColor = true
	if hb := strings.Join(m.artColumn(s, 298, 4), "\n"); !strings.Contains(hb, "▀") {
		t.Error("a failed kitty encode should degrade to half-blocks under truecolor")
	}

	// no truecolor: it falls all the way back to the motif (built == nil)
	m2 := artModel(t)
	m2.cfg.ArtMode = "kitty"
	m2.sty.kittyGraphics = true
	m2.sty.trueColor = false
	mo := strings.Join(m2.artColumn(s, 298, 4), "\n")
	if !strings.Contains(mo, "█") || strings.Contains(mo, "▀") {
		t.Errorf("a failed kitty encode without truecolor should fall back to the motif")
	}
}

func TestCov_ghostCoverKittyDegrade(t *testing.T) {
	img := fillImg(40, 40, color.RGBA{200, 60, 40, 255})
	s := protocol.Snapshot{Connected: true, LastArt: img, LastCoverURL: "u"}

	m := artModel(t)
	m.cfg.ArtMode = "kitty"
	m.sty.kittyGraphics = true
	m.sty.trueColor = true
	if hb := strings.Join(m.ghostCover(s, 298, 4), "\n"); !strings.Contains(hb, "▀") {
		t.Error("a failed kitty ghost encode should degrade to half-blocks under truecolor")
	}

	m2 := artModel(t)
	m2.cfg.ArtMode = "kitty"
	m2.sty.kittyGraphics = true
	m2.sty.trueColor = false
	if g := m2.ghostCover(s, 298, 4); g != nil {
		t.Error("a failed kitty ghost encode without truecolor should be nil")
	}
}

func TestCov_diagCardsNarrowSparkW(t *testing.T) {
	// driving the card grid below its width floor squeezes the per-target sparkline
	// column below its minimum (the sw<4 clamp in the latency-card builder).
	m, st := richDiag(t, "5180", nil)
	m.sty = newTheme()
	m.rows = 44
	if stripANSI(m.renderDiagCards(st.Snap(), time.Now(), 50)) == "" {
		t.Error("narrow cards should still render")
	}
}
