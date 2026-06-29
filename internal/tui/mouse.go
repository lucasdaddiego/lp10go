package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/lucasdaddiego/lp10/internal/tunnel"
)

// The dashboard frame fills the whole window (View's lipgloss.Place receives
// content already sized to cols×rows), so the body has a fixed origin: the left
// border (1) + left padding (2), and the top border (1) + top padding (0 — the
// frame is symmetric, content flush to the top and bottom borders). Every hit-zone
// is recorded in these absolute terminal coordinates, matching the X/Y a
// tea.MouseMsg carries.
const (
	bodyX0 = 3
	bodyY0 = 1
)

// rect is a half-open cell rectangle [x,x+w) × [y,y+h) in absolute coords.
type rect struct{ x, y, w, h int }

// hit reports whether (x,y) lies in the rect. A zero-area rect never hits, so an
// unpopulated zone (e.g. the volume rail in the compact layout) is inert.
func (r rect) hit(x, y int) bool {
	return r.w > 0 && r.h > 0 && x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

// btnZone is a discrete press target (a transport or mute button). action is the
// do() verb; focusIdx is the index into actions for keyboard-focus parity, or -1
// for a button outside that ring (mute).
type btnZone struct {
	rect
	action   string
	focusIdx int
}

// volZone is the volume control. vertical is the full-layout rail (click/drag Y
// sets the level); the compact horizontal meter is not registered (scroll adjusts
// volume there instead), so a zero volZone is simply inert.
type volZone struct {
	rect
	vertical bool
}

// eqZone is one EQ band column. rect is the whole band (a click focuses it); bar
// is just the bar squares (a click/drag there sets a ranged control by position).
// toggle marks an on/off band; min/max bound a ranged one; code is its wire code.
type eqZone struct {
	rect
	bar    rect
	d      int // display position (index into eqOrder), for eqFocus
	code   string
	toggle bool
	min    int
	max    int
}

// fracToVol maps a 0..1 fraction to a clamped 0..100 percentage (rounded).
func fracToVol(frac float64) int {
	v := int(frac*100 + 0.5)
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// hfrac maps an absolute x onto a fill rectangle r: the left edge reads 0.0 and
// the right edge reads 1.0 (left→min value, right→max value for a horizontal
// slider). Endpoints are reachable; values outside r are clamped.
func hfrac(r rect, x int) float64 {
	den := r.w - 1
	if den < 1 {
		den = 1
	}
	f := float64(x-r.x) / float64(den)
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// vfrac maps an absolute y onto a fill rectangle r so the top row reads full and
// the bottom row empty (the inverse of a bottom-up bar). Endpoints are reachable:
// clicking the top sets 1.0, the bottom 0.0.
func vfrac(r rect, y int) float64 {
	den := r.h - 1
	if den < 1 {
		den = 1
	}
	f := float64(den-(y-r.y)) / float64(den)
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// handleMouse dispatches one mouse event against the zones recorded by the last
// render. The frame the user clicked is the frame they see, so last-render zones
// are the correct ones. Press fires discrete buttons; press-or-drag sets the
// position controls (volume rail, ranged EQ bands); the wheel nudges the control
// under the cursor, falling back to volume.
func (m *model) handleMouse(e tea.MouseMsg) {
	// The diagnostics overlay swallows the mouse like it swallows keys: a left
	// click dismisses it, everything else is ignored while it's up.
	if m.diag {
		if e.Action == tea.MouseActionPress && e.Button == tea.MouseButtonLeft {
			m.diag = false
		}
		return
	}

	x, y := e.X, e.Y

	// Wheel: adjust the EQ band under the cursor, else the volume.
	if e.Button == tea.MouseButtonWheelUp || e.Button == tea.MouseButtonWheelDown {
		dir := 1
		if e.Button == tea.MouseButtonWheelDown {
			dir = -1
		}
		for _, z := range m.mzEQ {
			if z.hit(x, y) {
				m.pane, m.eqFocus = paneEQ, z.d
				m.eqAdjust(dir)
				return
			}
		}
		if dir > 0 {
			m.do("volup")
		} else {
			m.do("voldn")
		}
		return
	}

	leftPress := e.Action == tea.MouseActionPress && e.Button == tea.MouseButtonLeft
	leftDrag := e.Action == tea.MouseActionMotion && e.Button == tea.MouseButtonLeft
	leftActive := leftPress || leftDrag
	if !leftActive {
		return
	}

	// Volume rail: set by vertical position (press or drag to scrub).
	if m.mzVol.vertical && m.mzVol.hit(x, y) {
		m.setVol(fracToVol(vfrac(m.mzVol.rect, y)))
		return
	}

	// EQ bands: a click focuses the band; on a toggle it flips (press only, so a
	// drag doesn't chatter); on a ranged band a click/drag on the bar sets it.
	for _, z := range m.mzEQ {
		if !z.hit(x, y) {
			continue
		}
		m.pane, m.eqFocus = paneEQ, z.d
		switch {
		case z.toggle:
			if leftPress {
				m.eqToggleFocused()
			}
		case z.bar.hit(x, y):
			frac := hfrac(z.bar, x) // horizontal slider: left=min, right=max
			m.sendEQ(z.code, tunnel.Clamp(z.code, z.min+int(frac*float64(z.max-z.min)+0.5)))
		}
		return
	}

	// Transport / mute buttons fire on press only (a drag across them is a no-op).
	if leftPress {
		for _, z := range m.mzBtns {
			if z.hit(x, y) {
				if z.focusIdx >= 0 {
					m.pane, m.focus = paneNow, z.focusIdx
				}
				m.do(z.action)
				return
			}
		}
	}
}
