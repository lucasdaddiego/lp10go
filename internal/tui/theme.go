package tui

import (
	"fmt"
	"image/color"
	"math"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/lucasdaddiego/lp10/internal/artwork"
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

	warm []lipgloss.Style // EQ boost ramp (tone > 0): amber -> gold
	cool []lipgloss.Style // EQ cut ramp (tone < 0): indigo -> sky

	trueColor     bool // terminal advertises 24-bit color (gates the half-block album art)
	kittyGraphics bool // terminal supports the Kitty graphics protocol (true-pixel album art)

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
	t.track = fg("#3a4150") // empty meter/rail cells: a visible grey, not near-black
	t.head = fg("#8af0d4")
	// Tone ramps for the graphic-EQ slider knob: a boosted band reads warm, a cut
	// band reads cool — so the sign of a tone control is legible at a glance.
	for _, h := range []string{"#6e4a12", "#9c6a1d", "#c98a2b", "#e6a83e", "#ffc861"} {
		t.warm = append(t.warm, fg(h))
	}
	for _, h := range []string{"#26408a", "#2f57b0", "#3f74d6", "#5f95ee", "#86b6ff"} {
		t.cool = append(t.cool, fg(h))
	}
	t.trueColor = termenv.EnvColorProfile() == termenv.TrueColor
	t.kittyGraphics = detectKittyGraphics()
	t.btnOn = lipgloss.NewStyle().Foreground(lipgloss.Color("#06231b")).Background(lipgloss.Color("#34d9ad")).Bold(true).Padding(0, 1)
	t.btnOff = lipgloss.NewStyle().Foreground(lipgloss.Color("#6b7480")).Padding(0, 1)
	t.segOn = lipgloss.NewStyle().Foreground(lipgloss.Color("#06231b")).Background(lipgloss.Color("#34d9ad")).Bold(true)
	t.segOff = lipgloss.NewStyle().Foreground(lipgloss.Color("#aab3c0")).Background(lipgloss.Color("#1c222c"))
	return t
}

// detectKittyGraphics reports whether the terminal is known to support the Kitty
// graphics protocol with Unicode placeholders. There's no in-band capability
// query before Bubble Tea seizes the terminal, so this goes by environment
// fingerprint: Ghostty and kitty both implement it; everything else falls back
// to the half-block raster. A false negative just means half-blocks (still real
// art); kitty/auto can be forced via the art_mode config.
func detectKittyGraphics() bool {
	// A multiplexer inherits the host terminal's env (KITTY_WINDOW_ID, GHOSTTY_*)
	// but doesn't pass the graphics protocol through, so don't trust those vars
	// under tmux/screen — the half-block raster renders correctly there.
	if os.Getenv("TMUX") != "" {
		return false
	}
	if t := os.Getenv("TERM"); strings.HasPrefix(t, "screen") || strings.HasPrefix(t, "tmux") {
		return false
	}
	if os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("GHOSTTY_RESOURCES_DIR") != "" || os.Getenv("GHOSTTY_BIN_DIR") != "" {
		return true
	}
	switch strings.ToLower(os.Getenv("TERM_PROGRAM")) {
	case "ghostty", "kitty":
		return true
	}
	term := strings.ToLower(os.Getenv("TERM"))
	return strings.Contains(term, "kitty") || strings.Contains(term, "ghostty")
}

// ambientTint is a per-album recolouring derived from the cover's dominant hue:
// a fill gradient + head for the seek bar, plus a dim pen for the cover frame.
// nil means "use the theme defaults" (no cover, a greyscale cover, or art
// disabled). The connected status dot deliberately stays the theme green — it's a
// status light, so it must not drift to an album hue that reads as a warning.
type ambientTint struct {
	fill  []lipgloss.Style
	head  lipgloss.Style
	frame lipgloss.Style
}

// tint derives an ambientTint from a cover's representative colour c. Only the
// hue (and a clamped saturation) ride along from c; lightness is swept across a
// fixed dark→bright ramp, so the seek bar keeps the same readable contrast as the
// default teal — just in the album's colour. Saturation is floored so even a
// muted cover still reads as tinted, and ceilinged so a neon cover never glares.
func (t *theme) tint(c color.RGBA) *ambientTint {
	h, s, _ := artwork.RGBToHSL(c.R, c.G, c.B)
	s = clampRange(s, 0.35, 0.85)
	pen := func(h, s, l float64) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(hslHex(h, s, l)))
	}
	at := &ambientTint{
		head:  pen(h, math.Min(s+0.1, 1), 0.78),
		frame: pen(h, s*0.55, 0.44),
	}
	for _, l := range []float64{0.26, 0.34, 0.44, 0.55, 0.68} {
		at.fill = append(at.fill, pen(h, s, l))
	}
	return at
}

func clampRange(v, lo, hi float64) float64 { return max(lo, min(hi, v)) }

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
	for y := range h {
		fy := float64(y)
		var b strings.Builder
		for x := range w {
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

// sonar renders a w×h animated radar sweep: a beam rotates from a bright central
// hub, brightening toward the rim and dragging a fading comet-wedge behind it,
// over a faint static rim ring (the "scope" boundary). Advanced by frame, in the
// app's teal. Shown in the idle cover slot while (re)connecting, so the box reads
// as "scanning for the device". Cells are ~2:1, so the vertical delta is doubled
// to keep the sweep circular. Intensity is carried by colour brightness on dot
// glyphs (no block-shade fill), so it reads as a clean glow rather than a raster.
// Each line is exactly w display columns.
func (t *theme) sonar(w, h, frame int) []string {
	if w <= 0 || h <= 0 {
		return nil
	}
	cx, cy := float64(w-1)/2, float64(h-1)/2
	fitR := math.Min(cx, cy*2) // the largest scope circle that fits the box (cells are ~2:1)
	if fitR < 1 {
		fitR = 1
	}
	theta := float64(frame) * 0.13 // beam angle (radians); ~1 turn / 3.5s at the sonar fps
	bx, by := math.Cos(theta), math.Sin(theta)
	const (
		beamHW = 2.0  // beam half-width (doubled-y units): a thin line, not a wedge
		trail  = 1.6  // angular length of the fading comet-tail behind the beam (radians)
		floor  = 0.12 // below this a cell is black — crisp gaps, no grain
		hubR   = 0.13 // fraction of fitR for the bright central hub
		ringR  = 0.95 // fraction of fitR for the faint static scope ring
	)
	lines := make([]string, h)
	for y := range h {
		var b strings.Builder
		for x := range w {
			dx := float64(x) - cx
			dy := (float64(y) - cy) * 2 // double dy so the scope is circular on ~2:1 cells
			rr := math.Hypot(dx, dy)
			r := rr / fitR // 0..1 inside the scope, >1 in the corners (left black)
			inten := 0.0
			if r <= 1 && rr > 0 {
				// the beam: a thin radial line at angle theta, by perpendicular distance
				// (so it stays one clean stroke at every radius, never a filled wedge).
				if along := dx*bx + dy*by; along > 0 {
					perp := -dx*by + dy*bx
					if line := 1 - math.Abs(perp)/beamHW; line > 0 {
						inten = line * (0.40 + 0.60*r) // a reaching stroke, brightest at the rim
					}
				}
				// a faint comet-tail: an afterglow arc hugging the rim just behind the beam
				// (gated to the outer radius so it trails as an arc, not a pie slice).
				d := math.Mod(theta-math.Atan2(dy, dx), 2*math.Pi)
				if d < 0 {
					d += 2 * math.Pi
				}
				if d < trail && r > 0.6 {
					tail := (1 - d/trail) * (r - 0.6) / 0.4 * 0.45
					if tail > inten {
						inten = tail
					}
				}
			}
			// faint static scope ring at the boundary
			if ring := 0.22 * (1 - math.Abs(r-ringR)/0.05); ring > inten {
				inten = ring
			}
			// bright central hub
			if r < hubR {
				if hub := 1 - r/hubR; hub > inten {
					inten = hub
				}
			}
			if inten < floor {
				b.WriteByte(' ')
				continue
			}
			glyph := "·"
			if inten > 0.6 {
				glyph = "●" // a solid core for the beam head and the hub
			}
			b.WriteString(lipgloss.NewStyle().
				Foreground(lipgloss.Color(hslHex(168, 0.40+0.30*inten, 0.20+0.55*inten))).Render(glyph))
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
func to8(v float64) int { return max(0, min(255, int(v*255+0.5))) }

// lineMeter renders a horizontal meter cells wide: a gradient filled run, a
// bright head, then a dim track — used for the seek and volume bars. The fill
// ramps over the *played* length (not the whole bar), so it darkens at the start
// and brightens up to the playhead regardless of progress.
func (t *theme) lineMeter(frac float64, cells int) string {
	return t.lineMeterPen(frac, cells, t.fill, t.head)
}

// lineMeterPen is lineMeter with an explicit fill gradient and head colour, so
// the seek bar can be recoloured to the album's ambient hue while the volume
// meter keeps the default teal.
func (t *theme) lineMeterPen(frac float64, cells int, fill []lipgloss.Style, head lipgloss.Style) string {
	if cells <= 0 {
		return ""
	}
	frac = clampF(frac)
	h := int(math.Round(frac * float64(cells)))
	var b strings.Builder
	for i := range cells {
		switch {
		case i == h-1 || (h == 0 && i == 0):
			b.WriteString(head.Render("●"))
		case i < h-1:
			b.WriteString(t.rampAt(fill, i, h).Render("━"))
		default:
			b.WriteString(t.track.Render("─"))
		}
	}
	return b.String()
}

// gaugeBar renders a horizontal LINE meter cells wide — a heavy rule (GL["fill"]
// "━") in the health colour over a light rule (GL["track"] "─") in dim grey, like
// the seek bar and EQ sliders. A thin centred line (rather than a full-height
// block) keeps vertically-stacked gauges from merging into one solid region, and
// the heavy/light weight difference still distinguishes fill from track on a
// no-colour terminal. The caller picks the fill colour for health.
func (t *theme) gaugeBar(frac float64, cells int, fillPen lipgloss.Style) string {
	frac = clampF(frac)
	n := int(math.Round(frac * float64(cells)))
	var b strings.Builder
	for i := range cells {
		if i < n {
			b.WriteString(fillPen.Render(GL["fill"]))
		} else {
			b.WriteString(t.track.Render(GL["track"]))
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
	for row := range h {
		fromBottom := h - 1 - row
		if fromBottom < filled {
			lines[row] = t.rampAt(t.fill, fromBottom, h).Render("█")
		} else {
			lines[row] = t.track.Render("▓") // a visible grey channel, not a faint ░
		}
	}
	return lines
}

func clampF(f float64) float64 { return clampRange(f, 0, 1) }

// Shared alert tokens: the warn amber and the bold alarm red used across the
// player, EQ, and diagnostics views (header states, MUTED, error lines).
var (
	stWarn = lipgloss.NewStyle().Foreground(lipgloss.Color("#e0b34d"))
	stRed  = lipgloss.NewStyle().Foreground(lipgloss.Color("#e2655f")).Bold(true)
)
