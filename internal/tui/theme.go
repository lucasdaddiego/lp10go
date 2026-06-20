package tui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// theme holds the lipgloss styles and rendering primitives for the UI, built
// once per run. lipgloss downsamples the hex colors to the terminal's profile
// (256-color or, on a monochrome terminal, attribute-only — emphasis then falls
// back to the bold/reverse already baked into the prominent styles).
type theme struct {
	border lipgloss.Color

	sAcc lipgloss.Style // accent (teal)
	sBri lipgloss.Style // bright track title
	sTxt lipgloss.Style // body text
	sDim lipgloss.Style // dim metadata
	sDmr lipgloss.Style // dimmer hints

	fill  []lipgloss.Style // meter/bar gradient, dark -> bright
	track lipgloss.Style   // empty meter/bar cell
	head  lipgloss.Style   // meter position head

	gaugeEmpty string // diag-gauge empty cell: "█" (color-distinguished) or "░" on a no-color terminal

	btnOn  lipgloss.Style // focused button
	btnOff lipgloss.Style // unfocused button
	segOn  lipgloss.Style // focused segmented (33%) transport button
	segOff lipgloss.Style // unfocused segmented transport button
}

func newTheme() *theme {
	fg := func(hex string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(hex)) }
	t := &theme{border: lipgloss.Color("#4a5562")}
	t.sAcc = fg("#34d9ad")
	t.sBri = fg("#f0f2f5").Bold(true)
	t.sTxt = fg("#d7dbe2")
	t.sDim = fg("#6b7480")
	t.sDmr = fg("#515863")
	for _, h := range []string{"#157a63", "#1d9e75", "#2bbf94", "#34d9ad", "#5fe0bf"} {
		t.fill = append(t.fill, fg(h))
	}
	t.track = fg("#262b34")
	t.head = fg("#8af0d4")
	// The diag gauges encode fill with colour on a single "█" glyph. On a
	// no-colour terminal that's indistinguishable, so fall back to a shaded
	// glyph (same display width) for the empty cells.
	t.gaugeEmpty = "█"
	if termenv.EnvColorProfile() == termenv.Ascii {
		t.gaugeEmpty = "░"
	}
	t.btnOn = lipgloss.NewStyle().Foreground(lipgloss.Color("#06231b")).Background(lipgloss.Color("#34d9ad")).Bold(true).Padding(0, 1)
	t.btnOff = lipgloss.NewStyle().Foreground(lipgloss.Color("#6b7480")).Padding(0, 1)
	t.segOn = lipgloss.NewStyle().Foreground(lipgloss.Color("#06231b")).Background(lipgloss.Color("#34d9ad")).Bold(true)
	t.segOff = lipgloss.NewStyle().Foreground(lipgloss.Color("#aab3c0")).Background(lipgloss.Color("#1c222c"))
	return t
}

func (t *theme) rampAt(styles []lipgloss.Style, pos, span int) lipgloss.Style {
	r := 0.0
	if span > 1 {
		r = float64(pos) / float64(span-1)
	}
	i := int(r*float64(len(styles)-1) + 0.5)
	if i < 0 {
		i = 0
	} else if i >= len(styles) {
		i = len(styles) - 1
	}
	return styles[i]
}

// motifBlock renders a w×h animated "plasma" of block cells. A gentle domain warp
// braids the field like a lava lamp; the hue is a smooth low-spatial-frequency
// gradient — adjacent cells differ by only a few degrees, a continuous flow rather
// than confetti — plus a slow global rotation that cycles the whole spectrum over
// time. Brightness is layered sines. Advanced by frame (a frozen frame yields a
// still image, e.g. while paused). 24-bit truecolor; lipgloss downsamples on lesser
// terminals.
func (t *theme) motifBlock(w, h, frame int) []string {
	ph := float64(frame) * 0.16
	const warpAmp = 0.9
	lines := make([]string, h)
	for y := 0; y < h; y++ {
		fy := float64(y)
		var b strings.Builder
		for x := 0; x < w; x++ {
			fx := float64(x)
			// gentle low-frequency vector warp so the bands braid organically;
			// small coeffs + amp keep ax,ay within ~1 cell of fx,fy (stays smooth)
			wx := math.Sin(fy*0.30+ph*0.6) + 0.5*math.Sin(fx*0.22-ph*0.35)
			wy := math.Sin(fx*0.27-ph*0.5) + 0.5*math.Sin(fy*0.24+ph*0.45)
			ax, ay := fx+warpAmp*wx, fy+warpAmp*wy
			// brightness plasma on the warped coords; v in [-3,3] -> n in [0,1]
			v := math.Sin(ax*0.55+ph) + math.Sin(ay*0.75-ph*0.8) + math.Sin((ax+ay)*0.42+ph*1.3)
			n := (v + 3) / 6
			// hue: global spectrum rotation + a broad spatial gradient + a swirl,
			// all continuous (no random term) so neighbours stay within a few °
			hue := math.Mod(ph*57.29578+
				74*math.Sin(ax*0.20+ay*0.16+ph*0.5)+
				24*math.Sin((ax-ay)*0.17-ph*0.35)+360, 360)
			pen := lipgloss.NewStyle().Foreground(lipgloss.Color(hslHex(hue, 0.70+0.18*n, 0.20+0.40*n)))
			b.WriteString(pen.Render("█"))
		}
		lines[y] = b.String()
	}
	return lines
}

// hslHex converts HSL (h in degrees, s and l in 0..1) to a "#rrggbb" string.
func hslHex(h, s, l float64) string {
	c := (1 - math.Abs(2*l-1)) * s
	hp := math.Mod(h/60, 6)
	if hp < 0 {
		hp += 6
	}
	x := c * (1 - math.Abs(math.Mod(hp, 2)-1))
	var r, g, bl float64
	switch int(hp) {
	case 0:
		r, g, bl = c, x, 0
	case 1:
		r, g, bl = x, c, 0
	case 2:
		r, g, bl = 0, c, x
	case 3:
		r, g, bl = 0, x, c
	case 4:
		r, g, bl = x, 0, c
	default:
		r, g, bl = c, 0, x
	}
	m := l - c/2
	return fmt.Sprintf("#%02x%02x%02x", to8(r+m), to8(g+m), to8(bl+m))
}

// to8 maps a 0..1 channel to a clamped 0..255 byte.
func to8(v float64) int {
	switch {
	case v <= 0:
		return 0
	case v >= 1:
		return 255
	default:
		return int(v*255 + 0.5)
	}
}

// lineMeter renders a horizontal meter cells wide: a gradient filled run, a
// bright head, then a dim track — used for the seek and volume bars.
func (t *theme) lineMeter(frac float64, cells int) string {
	if cells <= 0 {
		return ""
	}
	frac = clampF(frac)
	head := int(math.Round(frac * float64(cells)))
	var b strings.Builder
	for i := 0; i < cells; i++ {
		switch {
		case i == head-1 || (head == 0 && i == 0):
			b.WriteString(t.head.Render("●"))
		case i < head-1:
			b.WriteString(t.rampAt(t.fill, i, cells).Render("━"))
		default:
			b.WriteString(t.track.Render("─"))
		}
	}
	return b.String()
}

// gaugeBar renders a horizontal meter cells wide in fillPen on a dim track — the
// diagnostics gauges. Filled and empty cells use the SAME "█" glyph (only the
// colour differs), so the bar is one uniform-width two-tone bar rather than a
// solid fill against a dotted track that reads wider/busier. The caller picks the
// fill colour for health.
func (t *theme) gaugeBar(frac float64, cells int, fillPen lipgloss.Style) string {
	frac = clampF(frac)
	n := int(math.Round(frac * float64(cells)))
	var b strings.Builder
	for i := 0; i < cells; i++ {
		if i < n {
			b.WriteString(fillPen.Render("█"))
		} else {
			b.WriteString(t.track.Render(t.gaugeEmpty))
		}
	}
	return b.String()
}

// vbar renders a vertical bar h rows tall (top line first) for a graphic-EQ
// band: filled from the bottom, brighter toward the top of the fill.
func (t *theme) vbar(frac float64, h int) []string {
	frac = clampF(frac)
	filled := int(math.Round(frac * float64(h)))
	lines := make([]string, h)
	for row := 0; row < h; row++ {
		fromBottom := h - 1 - row
		if fromBottom < filled {
			lines[row] = t.rampAt(t.fill, fromBottom, h).Render("█")
		} else {
			lines[row] = t.track.Render("░")
		}
	}
	return lines
}

func clampF(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
