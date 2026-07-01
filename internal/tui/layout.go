// Width-aware layout primitives shared by the player views and the diagnostics
// overlay: padding, centring, and column splitting. Two width vocabularies
// coexist deliberately — DispW for plain text, lipgloss.Width for styled text
// (see padDisp vs padVis).

package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ccell centres an already-styled string in a colW-wide block via lipgloss
// (display-width aware, ANSI-safe), so labels, bars, and values of differing
// widths line up identically — the volume rail and the EQ bands lean on it.
func ccell(s string, colW int) string {
	return lipgloss.NewStyle().Width(colW).Align(lipgloss.Center).Render(s)
}

// padDisp right-pads s with spaces to display width w (a no-op if already ≥ w).
// For PLAIN text only — use padVis for already-styled strings (DispW counts the
// bytes of any ANSI escapes, which over-measures a styled string).
func padDisp(s string, w int) string {
	if d := w - DispW(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// rpadDisp left-pads s with spaces to display width w (right-justify); no-op if ≥ w.
func rpadDisp(s string, w int) string {
	if d := w - DispW(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

// padVis right-pads a (possibly ANSI-styled) string to visible width w, measuring
// with lipgloss.Width so colour escapes aren't counted. The diag cards lean on it
// to keep their borders aligned once styling is applied on a real terminal.
func padVis(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// between places left- and right-aligned styled segments W columns apart, using
// the segments' known visible widths (styled strings carry ANSI codes).
func between(left string, leftW int, right string, rightW int, W int) string {
	gap := max(W-leftW-rightW, 1)
	return left + strings.Repeat(" ", gap) + right
}

// splitWidth divides total into n column widths summing to total (earlier
// columns get the remainder).
func splitWidth(total, n int) []int {
	base, extra := total/n, total%n
	w := make([]int, n)
	for i := range w {
		w[i] = base
		if i < extra {
			w[i]++
		}
	}
	return w
}
