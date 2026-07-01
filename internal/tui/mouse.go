package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/lucasdaddiego/lp10/internal/protocol"
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
func fracToVol(frac float64) int { return max(0, min(100, int(frac*100+0.5))) }

// hfrac maps an absolute x onto a fill rectangle r: the left edge reads 0.0 and
// the right edge reads 1.0 (left→min value, right→max value for a horizontal
// slider). Endpoints are reachable; values outside r are clamped.
func hfrac(r rect, x int) float64 {
	den := max(r.w-1, 1)
	return clampF(float64(x-r.x) / float64(den))
}

// vfrac maps an absolute y onto a fill rectangle r so the top row reads full and
// the bottom row empty (the inverse of a bottom-up bar). Endpoints are reachable:
// clicking the top sets 1.0, the bottom 0.0.
func vfrac(r rect, y int) float64 {
	den := max(r.h-1, 1)
	return clampF(float64(den-(y-r.y)) / float64(den))
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

// ---- hit-zone recorders (the render-side counterpart of handleMouse) ----

// recordFullZones records the transport, volume-rail, and EQ-band hit-zones for
// the full dashboard, in absolute terminal coordinates. It mirrors the geometry
// of renderDashboard's full branch: the block is vertically centred by stack
// between a two-line top ([header, ""]) and the bottom tail, and the bottom tail
// (EQ sliders + footer) is pinned to the inner region's foot.
func (m *model) recordFullZones(coverW, midW, blockH, midLen, tailLen, inner, W int) {
	// stack's middle region (below [header,""], above tail)
	region := max(inner-2-tailLen, 0)
	// stack clips the block to the region if it overflows
	middleLen := min(blockH, region)
	blockTop := bodyY0 + 2 + (region-middleLen)/2 // 2 = the [header, ""] top rows

	// Transport buttons: the last line of the now-playing block, which frameBody
	// centres in the blockH-tall column (top = (blockH-midLen)/2 when it fits) — so
	// the zone tracks exactly where transportSegments was drawn.
	midTop := 0
	if midLen < blockH {
		midTop = (blockH - midLen) / 2
	}
	if row := midTop + midLen - 1; row >= 0 && row < middleLen { // visible (not clipped)
		pad, widths, gap := transportLayout(midW)
		x := bodyX0 + coverW + 2 + artGap + pad
		y := blockTop + row
		for i, a := range actions {
			m.mzBtns = append(m.mzBtns, btnZone{rect{x, y, widths[i], 1}, a, i})
			x += widths[i] + gap // skip the inter-button gap
		}
	}

	// Volume rail: the bar squares (volRail draws blockH-1 of them, then a value
	// row that isn't part of the zone).
	if h := blockH - 1; h > 0 {
		if h > middleLen {
			h = middleLen
		}
		if h > 0 {
			volX := bodyX0 + coverW + 2 + artGap + midW + artGap
			m.mzVol = volZone{rect{volX, blockTop, volColW, h}, true}
		}
	}

	// EQ rows in the bottom tail. tail = [divider, band0, …, band6, footer, errLine?];
	// bands start one line in (immediately after the divider).
	eqTop := bodyY0 + inner - tailLen + 1
	trackW := max(W-sliderLabelW-sliderValW, 1)
	for d := range eqOrder {
		sp := tunnel.Specs[eqOrder[d]]
		y := eqTop + d
		m.mzEQ = append(m.mzEQ, eqZone{
			rect:   rect{bodyX0, y, W, 1},
			bar:    rect{bodyX0 + sliderLabelW, y, trackW, 1},
			d:      d,
			code:   sp.Code,
			toggle: sp.Kind == tunnel.Toggle,
			min:    sp.Min,
			max:    sp.Max,
		})
	}
}

// recordCompactZones records the transport + mute button hit-zones for the
// compact dashboard. The content is top-pinned (frameBody, no centring), so the
// controls row sits a fixed offset below the header; volume is left to the wheel.
func (m *model) recordCompactZones(s protocol.Snapshot, metaLen, tailLen, inner, W int) {
	row := metaLen + 5 // [header, "", meta…, "", seek, "", controls]
	if row >= inner-tailLen {
		return // clipped off by the pinned tail
	}
	y := bodyY0 + row
	// Mirror controlsRow's button widths exactly (toggleVerb + btn's 2-col padding).
	pvW, tgW, nxW := DispW(GL["rew"])+2, DispW(toggleVerb(s))+2, DispW(GL["ff"])+2
	x := bodyX0
	for _, b := range []struct {
		action string
		w, idx int
	}{{"prev", pvW, 0}, {"toggle", tgW, 1}, {"next", nxW, 2}} {
		m.mzBtns = append(m.mzBtns, btnZone{rect{x, y, b.w, 1}, b.action, b.idx})
		x += b.w + 1 // a single space separates the buttons
	}
	// The mute button is the last element of the right-aligned cluster, so it
	// occupies the final mtW columns regardless of the volume value's width.
	muteLbl := "mute"
	if s.Muted {
		muteLbl = "unmute"
	}
	mtW := DispW(muteLbl) + 2
	m.mzBtns = append(m.mzBtns, btnZone{rect{bodyX0 + W - mtW, y, mtW, 1}, "mute", -1})
}
