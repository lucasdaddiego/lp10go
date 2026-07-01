package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// On a wide terminal the diagnostics overlay uses the two-column card grid and
// surfaces the new device metrics (audio chain, scheduler contention, CPU clock),
// and every framed line stays exactly cols wide. Crucially this runs under a real
// colour profile so the rows carry ANSI — the card padding must measure visible
// width (lipgloss.Width), not byte width (DispW), or the borders scatter (the bug
// that only showed on a truecolor terminal, never in the default Ascii test profile).
func TestDiagCardsLayoutWide(t *testing.T) {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(old)

	st := protocol.NewState()
	applyFixtureRecords(st, "device_record.txt")  // @@i: eth link + dns
	applyFixtureRecords(st, "playing_record.txt") // @@s: audio-chain tail
	m, _, _ := modelWith(st)
	m.rows, m.cols = 40, 120
	m.diag = true

	view := m.View()
	flat := clean(view)
	for _, want := range []string{
		"diagnostics",
		"─ audio", "─ connection", "─ device", "─ hardware", "─ latency", "─ network",
		"─ resources", "─ services",
		"dac", "S16_LE", "● live", "buffer",
		"tasks", "running", "1200 MHz",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("wide diag missing %q", want)
		}
	}
	// the two columns sit side by side and the sections run alphabetically: with
	// this fixture's heights the balanced split puts audio/connection/device/
	// hardware left and latency/network/resources/services right, so audio (first
	// left) shares its row with latency (first right).
	if !hasRow(flat, "─ audio", "─ latency") {
		t.Error("wide diag should place the audio and latency section heads side by side")
	}
	// alphabetical flow: audio tops the left column, i.e. it renders above device.
	if strings.Index(flat, "─ audio") > strings.Index(flat, "─ device") {
		t.Error("sections should run alphabetically (audio before device)")
	}
	// every framed line stays exactly cols wide (section padding measured by visible width)
	for i, ln := range strings.Split(view, "\n") {
		if w := lipgloss.Width(ln); w != m.cols {
			t.Errorf("diag line %d width %d, want %d: %q", i, w, m.cols, clean(ln))
		}
	}

	// A narrow terminal falls back to the single-column stacked read-out.
	m.cols = 90
	if hasRow(clean(m.View()), "─ audio", "─ latency") {
		t.Error("narrow diag should stack the sections, not place them side by side")
	}
}
