package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// hasRow reports whether some line of flat contains every one of subs.
func hasRow(flat string, subs ...string) bool {
	for ln := range strings.SplitSeq(flat, "\n") {
		ok := true
		for _, s := range subs {
			if !strings.Contains(ln, s) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// The diagnostics overlay carries the device's streaming-capability matrix (read
// live from @@c) as a grouped on/off strip and a static hardware reference,
// alongside the live metrics. Runs under a real colour profile so the boxless
// section padding is measured by visible width; every framed line stays cols wide.
func TestDiagShowsServicesAndHardware(t *testing.T) {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(old)

	st := protocol.NewState()
	applyFixtureRecords(st, "device_record.txt")  // @@i: eth link, platform
	applyFixtureRecords(st, "playing_record.txt") // @@s: live metrics
	applyFixtureRecords(st, "config_record.txt")  // @@c: the capability matrix
	m, _, _ := modelWith(st)
	m.rows, m.cols = 44, 120
	m.diag = true

	flat := clean(m.View())
	for _, want := range []string{
		"─ services", "─ hardware",
		"Spotify", "Bluetooth", "Google Cast", "USB playback",
		"Amlogic A113L", "WM8904", "no power amp", // hardware facts (the teardown knowledge)
		"env-gated · toggle in the Arylic app",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("diag overlay missing %q", want)
		}
	}
	// LibreWireless reference-image baggage the LP10 doesn't market must not appear.
	for _, absent := range []string{"Alexa", "Matter", "Roon"} {
		if strings.Contains(flat, absent) {
			t.Errorf("diag overlay should not list non-marketed %q", absent)
		}
	}
	// grouped strip: every enabled service shares the "on" row, disabled the "off" row.
	if !hasRow(flat, "Spotify", "Bluetooth") {
		t.Error("enabled services should share one dense on-row")
	}
	if !hasRow(flat, "Google Cast", "USB playback") {
		t.Error("disabled services should share one dense off-row")
	}
	for i, ln := range strings.Split(m.View(), "\n") {
		if w := lipgloss.Width(ln); w != m.cols {
			t.Errorf("diag line %d width %d, want %d: %q", i, w, m.cols, clean(ln))
		}
	}
}

// The masthead stays minimal: the title, a one-glance health verdict, and the
// connection light + clock — no inline vitals (every live number lives in its
// section below) — and the footer legend decodes the verdict colours.
func TestDiagMastheadVerdictOnly(t *testing.T) {
	st := protocol.NewState()
	applyFixtureRecords(st, "device_record.txt")
	applyFixtureRecords(st, "playing_record.txt") // healthy metrics
	m, _, _ := modelWith(st)
	m.rows, m.cols = 44, 120
	m.diag = true
	flat := clean(m.View())
	// masthead verdict next to the title
	if !hasRow(flat, "diagnostics", "healthy") {
		t.Error("masthead should carry a health verdict beside the title")
	}
	// ...and nothing else rides the title row
	for _, vital := range []string{"temp", "cpu", "mem", "buffer", "volume"} {
		if hasRow(flat, "diagnostics", vital) {
			t.Errorf("the title row should not carry a %q vital", vital)
		}
	}
	// the colour legend rides the footer
	if !hasRow(flat, "good", "warn", "fault") {
		t.Error("footer should carry the good/warn/fault legend")
	}
}

// The health verdict rolls up the worst live metric: a hot SoC reads "warn", a
// very hot one "fault".
func TestDiagVerdictWarnAndFault(t *testing.T) {
	mk := func(tempmC string) string {
		st := protocol.NewState()
		applyFixtureRecords(st, "device_record.txt")
		// everything healthy except the SoC temperature (field 9); @@v marks the
		// session connected + fresh so the verdict is shown.
		applyRaw(st, "@@s\n100 0.4 0.3 0.3 139000 221064 2 AR241CE_9243.16 Linux-5.15.137 "+
			tempmC+" 1 2 - - 2.0 - - RUNNING 4834 44100 S16_LE 2 22050 1200000 1/200 -\n@@E\n")
		applyRaw(st, "@@v\nMID-Read:64 Data:44 Length:2\n@@E\n")
		m, _, _ := modelWith(st)
		m.rows, m.cols = 44, 120
		m.diag = true
		return clean(m.View())
	}
	if !hasRow(mk("70000"), "diagnostics", "warn") {
		t.Error("a hot SoC (70 °C) should roll up to a warn verdict")
	}
	if !hasRow(mk("82000"), "diagnostics", "fault") {
		t.Error("a very hot SoC (82 °C) should roll up to a fault verdict")
	}
}

// An empty buffer while NOT playing is normal (idle ring), not a fault: it stays
// out of the verdict and reads neutral ("idle"), so an otherwise-healthy idle
// device still reads healthy.
func TestDiagIdleBufferNotFault(t *testing.T) {
	st := protocol.NewState()
	applyFixtureRecords(st, "device_record.txt")
	// PcmState SETUP (not RUNNING) + an empty ring (avail == size -> 0% fill).
	applyRaw(st, "@@s\n100 0.1 0.1 0.1 139000 221064 2 AR241CE_9243.16 Linux-5.15.137 "+
		"49000 1 2 - - 2.0 - - SETUP 22050 44100 S16_LE 2 22050 1200000 1/200 -\n@@E\n")
	applyRaw(st, "@@v\nMID-Read:64 Data:54 Length:2\n@@E\n")
	m, _, _ := modelWith(st)
	m.rows, m.cols = 44, 120
	m.diag = true
	flat := clean(m.View())
	if hasRow(flat, "diagnostics", "fault") {
		t.Error("an idle empty buffer must not roll up to a fault verdict")
	}
	if !hasRow(flat, "diagnostics", "healthy") {
		t.Error("an otherwise-healthy idle device should read healthy")
	}
	if !strings.Contains(flat, "idle") { // the buffer row is labelled idle, not "full"
		t.Error("an idle buffer row should be labelled idle")
	}
}

// Regression: the services strip must be width-budgeted in the STACKED (narrow)
// layout too, not only the cards path. With @@c loaded, the dense "on" row is ~54
// visible cols; at a narrow width it must be clipped to the body, never sized into
// a bordered frame wider than the terminal. (cols 58-59 with the full capability
// matrix used to overflow the frame — serviceStrip ignored its width budget.)
func TestDiagStackedServicesDoNotOverflowNarrow(t *testing.T) {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(old)

	st := protocol.NewState()
	applyFixtureRecords(st, "device_record.txt")
	applyFixtureRecords(st, "playing_record.txt")
	applyFixtureRecords(st, "config_record.txt") // @@c: four services on
	m, _, _ := modelWith(st)
	m.diag = true
	for _, cols := range []int{58, 59, 60, 64, 72} { // W < diagCardsMinW -> stacked
		m.rows, m.cols = 44, cols
		for i, ln := range strings.Split(m.View(), "\n") {
			if w := lipgloss.Width(ln); w != m.cols {
				t.Errorf("cols=%d: diag line %d width %d, want %d: %q", cols, i, w, m.cols, clean(ln))
			}
		}
	}
}

// The stacked (narrow) layout also carries the capability strip and the hardware
// reference.
func TestDiagStackedShowsServicesAndHardware(t *testing.T) {
	st := protocol.NewState()
	applyFixtureRecords(st, "device_record.txt")
	applyFixtureRecords(st, "playing_record.txt")
	applyFixtureRecords(st, "config_record.txt")
	m, _, _ := modelWith(st)
	m.rows, m.cols = 60, 90 // narrow -> stacked, tall enough for every section
	m.diag = true
	flat := clean(m.View())
	for _, want := range []string{"services", "hardware", "Spotify", "Bluetooth", "Amlogic A113L"} {
		if !strings.Contains(flat, want) {
			t.Errorf("stacked diag missing %q", want)
		}
	}
	if strings.Contains(flat, "Alexa") {
		t.Error("stacked diag should not list Alexa")
	}
}

// Before any @@c arrives the services strip shows a "reading…" note rather than
// naming capabilities it doesn't yet know.
func TestDiagServicesUnknownBeforeData(t *testing.T) {
	st := protocol.NewState()
	applyFixtureRecords(st, "device_record.txt")
	applyFixtureRecords(st, "playing_record.txt") // metrics present, but no @@c
	m, _, _ := modelWith(st)
	m.rows, m.cols = 44, 120
	m.diag = true
	flat := clean(m.View())
	if !strings.Contains(flat, "reading from device…") {
		t.Error("services should show the reading note before @@c arrives")
	}
	// the capability strip isn't rendered yet: a disabled service like Google Cast
	// only appears once @@c lands (the masthead's now-playing source doesn't count).
	if strings.Contains(flat, "Google Cast") {
		t.Error("no capability strip should render before @@c arrives")
	}
}
