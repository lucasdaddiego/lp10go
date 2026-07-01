package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// The Max-Vol-focused footer surfaces the teardown's MXV gotcha — a low output
// cap is what makes the IR remote / Spotify volume feel stuck — instead of the
// generic EQ hint; every other EQ slot keeps the generic hint.
func TestFooterRowMaxVolHint(t *testing.T) {
	m, _, _ := modelWith(protocol.NewState())
	m.sty = newTheme()
	m.pane = paneEQ
	m.eqFocus = len(eqOrder) - 1 // Max Vol is the last display slot
	got := stripANSI(m.footerRow(80))
	if !strings.Contains(got, "caps remote & Spotify volume") {
		t.Errorf("Max Vol footer hint = %q, want the output-cap warning", got)
	}
	m.eqFocus = 0 // any other band: the generic pick/adjust hint
	if got := stripANSI(m.footerRow(80)); !strings.Contains(got, "pick") {
		t.Errorf("non-MXV footer hint = %q, want the generic EQ hint", got)
	}
}

func TestWifiBand(t *testing.T) {
	for _, tc := range []struct{ freq, want string }{
		{"2437", " · ch 6 · 2.4 GHz"},
		{"5180", " · ch 36 · 5 GHz"},
		{"", ""},      // unknown frequency: no suffix
		{"bogus", ""}, // non-numeric: no suffix
	} {
		if got := wifiBand(tc.freq); got != tc.want {
			t.Errorf("wifiBand(%q) = %q, want %q", tc.freq, got, tc.want)
		}
	}
}

// The buffer gauge's detail word is self-explanatory: "NN% full" (of the ALSA
// ring buffer) while ALSA reports RUNNING; the stopped case reads "idle"
// (pinned by TestDiagIdleBufferNotFault).
func TestDiagPlayingBufferReadsFull(t *testing.T) {
	st := protocol.NewState()
	// PcmState RUNNING with avail 4834 of a 22050-frame ring -> ~78% full.
	applyRaw(st, "@@s\n100 0.1 0.1 0.1 139000 221064 2 AR241CE_9243.16 Linux-5.15.137 "+
		"49000 1 2 - - 2.0 - - RUNNING 4834 44100 S16_LE 2 22050 1200000 1/200 -\n@@E\n")
	applyRaw(st, "@@v\nMID-Read:64 Data:54 Length:2\n@@E\n")
	m, _, _ := modelWith(st)
	m.rows, m.cols = 44, 120
	m.diag = true
	flat := clean(m.View())
	if !strings.Contains(flat, "% full") {
		t.Errorf("playing buffer row should read \"NN%% full\":\n%s", flat)
	}
}

// A clipped styled row keeps its per-segment colours — it used to be stripped
// and re-rendered uniformly dim, so the services/eq rows "lost their colours"
// the moment a larger font cost the column a couple of cells. Needs a real
// colour profile so the styles emit SGR at all.
func TestClipStyledKeepsColours(t *testing.T) {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(old)
	sty := newTheme()
	row := sty.sAcc.Render("●") + " " + sty.sTxt.Render("Spotify and more text")
	got := clipStyled(row, 12)
	if w := lipgloss.Width(got); w > 12 {
		t.Errorf("clipped width = %d, want ≤ 12", w)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("clipped row lost its styling: %q", got)
	}
	if !strings.HasSuffix(stripANSI(got), GL["ell"]) {
		t.Errorf("clipped row should end with the ellipsis, got %q", stripANSI(got))
	}
}

// A service group wider than its column wraps onto aligned continuation rows
// instead of clipping, so every service stays visible (and coloured) at any
// width; only the trailing env-gated note may still clip.
func TestServiceStripWrapsNarrow(t *testing.T) {
	st := protocol.NewState()
	applyFixtureRecords(st, "config_record.txt")
	m, _, _ := modelWith(st)
	m.sty = newTheme()
	const w = 30
	rows := m.serviceStrip(w)
	joined := stripANSI(strings.Join(rows, "\n"))
	for _, sv := range confServices {
		if !strings.Contains(joined, sv.label) {
			t.Errorf("narrow services strip lost %q:\n%s", sv.label, joined)
		}
	}
	if len(rows) < 5 {
		t.Errorf("at w=%d both groups should wrap onto continuation rows, got %d rows", w, len(rows))
	}
	for i, r := range rows {
		if got := lipgloss.Width(r); got > w {
			t.Errorf("wrapped row %d width %d > %d: %q", i, got, w, stripANSI(r))
		}
		if i < len(rows)-1 && strings.Contains(stripANSI(r), GL["ell"]) {
			t.Errorf("service row %d should wrap, not clip: %q", i, stripANSI(r))
		}
	}
	// an empty group contributes no rows (not a bare label)
	if got := m.flowGroup("on", nil, w); got != nil {
		t.Errorf("flowGroup(empty) = %v, want nil", got)
	}
}

// The reg-90/92/39 readouts land as rows: name (FriendlyName), serial, bt MAC,
// mcu version, the fuller firmware string, and the multiroom state.
func TestDiagIdentityExtrasAndMultiroom(t *testing.T) {
	st := protocol.NewState()
	applyFixtureRecords(st, "device_record.txt") // @@i (name=) + @@d + @@g
	m, _, _ := modelWith(st)
	m.rows, m.cols = 44, 120
	m.diag = true
	flat := clean(m.View())
	for _, want := range []string{
		"Living",               // name row (FriendlyName, works without mDNS)
		"RKARYLLP100000000000", // serial
		"aa:bb:cc:dd:ee:fe",    // bt MAC
		"v16",                  // mcu version
		"AR241CE_9243.16.2",    // the fuller firmware string from reg 92
		"solo",                 // multiroom (empty group)
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("diag missing %q", want)
		}
	}
	if !hasRow(flat, "multiroom", "solo") {
		t.Error("the multiroom row should read solo for an empty group")
	}
}

func TestDiagMultiroomLinked(t *testing.T) {
	st := protocol.NewState()
	applyRaw(st, "@@g\nMID-Read:39 Data:{\"devices\":[{\"n\":\"a\"},{\"n\":\"b\"}]} Length:40\n@@E\n")
	m, _, _ := modelWith(st)
	m.rows, m.cols = 44, 120
	m.diag = true
	if flat := clean(m.View()); !hasRow(flat, "multiroom", "linked · 2 devices") {
		t.Errorf("linked group should read a device count")
	}
	applyRaw(st, "@@g\nMID-Read:39 Data:{\"devices\":[{\"n\":\"a\"}]} Length:23\n@@E\n")
	if flat := clean(m.View()); !hasRow(flat, "multiroom", "linked · 1 device") {
		t.Errorf("a single linked device should read singular")
	}
}

// The errors row shows SESSION deltas — the first sample baselines the boot
// lifetime (256 historical drops read as zero), growth turns the number amber.
func TestDiagErrorsRowSessionDelta(t *testing.T) {
	base := "@@s\n100 0.1 0.1 0.1 139000 221064 2 AR241CE_9243.16 Linux-5.15.137 " +
		"49000 1000 500 - - 2.0 - - RUNNING 4834 44100 S16_LE 2 22050 1200000 1/200 - "
	st := protocol.NewState()
	applyFixtureRecords(st, "device_record.txt")
	applyRaw(st, base+"0 0 256 0\n@@E\n")
	m, _, _ := modelWith(st)
	m.rows, m.cols = 44, 120
	m.diag = true
	if flat := clean(m.View()); !hasRow(flat, "errors", "drop 0", "session") {
		t.Error("the first sample should baseline: boot-lifetime drops read as 0")
	}
	applyRaw(st, base+"0 0 259 0\n@@E\n")
	if flat := clean(m.View()); !hasRow(flat, "errors", "drop 3") {
		t.Error("counter growth should read as a session delta of 3")
	}
}

// The category revamp: settings are not diagnostics — the volume and eq rows
// are gone (they live on the dashboard / EQ pane) — and each fact sits in the
// section that answers its question: the wire facts under network (mac) and
// connection (host · ssh · tunnel), runtime state under resources (uptime),
// identity only under device. Asserted in the stacked layout, where the single
// column makes divider order == section membership.
func TestDiagTaxonomy(t *testing.T) {
	st := protocol.NewState()
	applyFixtureRecords(st, "device_record.txt")
	applyFixtureRecords(st, "playing_record.txt")
	m, _, _ := modelWith(st)
	m.rows, m.cols = 60, 90 // narrow -> stacked, tall enough that nothing trims
	m.diag = true
	flat := clean(m.View())
	if strings.Contains(flat, "volume") || hasRow(flat, "eq        ") {
		t.Error("volume/eq are settings, not diagnostics — they must not render here")
	}
	lines := strings.Split(flat, "\n")
	idx := func(sub string) int {
		for i, ln := range lines {
			if strings.Contains(ln, sub) {
				return i
			}
		}
		t.Fatalf("diag missing %q", sub)
		return -1
	}
	inSection := func(row, sec, next string) {
		t.Helper()
		if r, a, b := idx(row), idx("─ "+sec+" ─"), idx("─ "+next+" ─"); r < a || r > b {
			t.Errorf("the %q row should sit in the %s section", row, sec)
		}
	}
	inSection("host", "connection", "device")
	inSection("ssh", "connection", "device")
	inSection("tunnel", "connection", "device")
	inSection("serial", "device", "hardware")
	inSection("mac", "network", "resources")
	inSection("uptime", "resources", "services")
	// the wide layout drops the settings rows too
	m.cols = 120
	if flat := clean(m.View()); strings.Contains(flat, "volume") {
		t.Error("the cards layout must not carry a volume row either")
	}
}

// A malformed running/total pair (no "/") hides the tasks row rather than
// rendering half a reading.
func TestTasksReadoutMalformed(t *testing.T) {
	m, _, _ := modelWith(protocol.NewState())
	if got := m.tasksReadout(&protocol.SysInfo{Procs: "237"}); got != "" {
		t.Errorf("tasksReadout(no slash) = %q, want empty", got)
	}
}

// Before any record arrives the connection section still reads sensibly:
// "no data yet" (not the old "rx — ago"), and the :2018 tunnel reads down.
func TestDiagConnectionPreData(t *testing.T) {
	m, _, _ := modelWith(protocol.NewState())
	m.rows, m.cols = 44, 120
	m.diag = true
	flat := clean(m.View())
	if !hasRow(flat, "ssh", "no data yet") {
		t.Error("the ssh row should read \"no data yet\" before the first record")
	}
	if !hasRow(flat, "tunnel", ":2018", "down") {
		t.Error("the tunnel row should read :2018 · down before the tunnel connects")
	}
}

func TestDiagFormat(t *testing.T) {
	if got := diagFormat(nil); got != "—" {
		t.Errorf("diagFormat(nil) = %q, want —", got)
	}
	// a track with neither quality nor channel info still reads as a dash
	if got := diagFormat(protocol.Track{"TrackName": "x"}); got != "—" {
		t.Errorf("diagFormat(bare track) = %q, want —", got)
	}
	tr := protocol.Track{"Mime": "Ogg", "SampleRate": 44100, "ChannelCount": 2}
	if got := diagFormat(tr); got != "Ogg · 44.1 kHz · 2 ch" {
		t.Errorf("diagFormat(full track) = %q", got)
	}
}
