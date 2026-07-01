// The equalizer pane: display order, the control verbs that ride the :2018
// tunnel, and the three renderers (full-dashboard sliders, the compact one-line
// summary, and the diagnostics readout).

package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/tunnel"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

// eqOrder maps EQ-strip display position -> index into tunnel.Specs, so the
// graphic equalizer reads EQ · Treble · Mid · Bass · Sub · Lvl · Max Vol while the
// wire-level Specs order is unchanged. Max Vol (the rarely-touched output cap) sits
// last.
var eqOrder = []int{1, 4, 3, 2, 5, 6, 0}

// eqShort is the compact band label per wire code.
var eqShort = map[string]string{"MXV": "Max Vol", "EQS": "EQ", "TRE": "Treble", "MID": "Mid", "BAS": "Bass", "VBS": "Sub", "VBI": "Lvl"}

// sliderLabelW / sliderValW are the fixed-width columns shared by eqSliders (the
// renderer) and recordFullZones (the mouse hit-zone builder). Both must agree so a
// click on the rendered slider maps to the correct track position.
const (
	sliderLabelW = 8 // "Max Vol " — label column: longest name "Max Vol" (7) + 1 space
	sliderValW   = 4 // right-aligned value column: " +10", " 100", "  -1", …
)

// eqSpec returns the tunnel.Spec for the focused EQ-strip position.
func (m *model) eqSpec() tunnel.Spec { return tunnel.Specs[eqOrder[m.eqFocus]] }

// eqCur is a control's last-known value (0 until the device has reported it).
func (m *model) eqCur(code string) int {
	if v, ok := m.st.EQValue(code); ok {
		return v
	}
	return 0
}

// eqAdjust nudges the focused control by dir*step, clamps it, and sends it.
func (m *model) eqAdjust(dir int) {
	sp := m.eqSpec()
	m.sendEQ(sp.Code, tunnel.Clamp(sp.Code, m.eqCur(sp.Code)+dir*sp.Step))
}

// eqToggleFocused flips a focused on/off control (no-op on ranged controls).
func (m *model) eqToggleFocused() {
	sp := m.eqSpec()
	if sp.Kind != tunnel.Toggle {
		return
	}
	v := 0
	if m.eqCur(sp.Code) == 0 {
		v = 1
	}
	m.sendEQ(sp.Code, v)
}

// sendEQ records the change optimistically (arming the echo hold) and enqueues
// the tunnel write, never blocking the update loop (drop-oldest like send).
func (m *model) sendEQ(code string, val int) {
	m.st.SetEQLocal(code, val)
	nbSend(m.eqcmds, workers.EQCommand{Code: code, Val: val})
}

// eqSliders renders one horizontal row per EQ band, all W columns wide, in
// display order. The rows are pinned to the bottom tail of the full dashboard.
func (m *model) eqSliders(s protocol.Snapshot, W int) []string {
	_, vals := m.st.EQView()
	rows := make([]string, len(eqOrder))
	for d, idx := range eqOrder {
		rows[d] = m.eqSliderRow(idx, vals, m.pane == paneEQ && m.eqFocus == d, W)
	}
	return rows
}

// eqSliderRow renders one EQ control as a W-wide horizontal row:
//
//	Toggle: "Label    ● on                        "
//	Ranged: "Label    ────────────●────────────  +3"
//
// The label column is sliderLabelW wide; the value column is sliderValW wide
// (right-aligned); the slider track fills the rest.
func (m *model) eqSliderRow(specIdx int, vals map[string]int, focused bool, W int) string {
	trackW := max(W-sliderLabelW-sliderValW, 1)
	sp := tunnel.Specs[specIdx]
	v, known := vals[sp.Code]

	// Label column: accent+bold when focused, dim otherwise.
	labelPen := m.sty.sDim
	if focused {
		labelPen = m.sty.sAcc.Bold(true)
	}
	raw := Clip(eqShort[sp.Code], sliderLabelW-1)
	labelCell := labelPen.Render(raw) + strings.Repeat(" ", sliderLabelW-DispW(raw))

	if sp.Kind == tunnel.Toggle {
		knob, state := "○", "off"
		knobPen, statePen := m.sty.sDmr, m.sty.sDmr
		if known && v != 0 {
			knob, state = "●", "on"
			knobPen, statePen = m.sty.sAcc, m.sty.sAcc
		}
		content := knobPen.Render(knob) + " " + statePen.Render(state)
		// pad content out to fill trackW + sliderValW (the right portion of the row)
		pad := max(trackW+sliderValW-1-1-DispW(state), 0)
		return labelCell + content + strings.Repeat(" ", pad)
	}

	// Ranged: a horizontal slider ────●────
	frac := 0.0
	if known && sp.Max > sp.Min {
		frac = float64(v-sp.Min) / float64(sp.Max-sp.Min)
	}
	knobPos := max(int(frac*float64(trackW-1)+0.5), 0)
	if knobPos >= trackW {
		knobPos = trackW - 1
	}

	// Knob colour: warm for a positive tone boost, cool for a cut, accent otherwise.
	knobPen := m.sty.sDim
	if focused {
		switch {
		case sp.Min < 0 && known && v > 0:
			knobPen = m.sty.warm[len(m.sty.warm)-1]
		case sp.Min < 0 && known && v < 0:
			knobPen = m.sty.cool[len(m.sty.cool)-1]
		default:
			knobPen = m.sty.sAcc
		}
	}
	left := strings.Repeat("─", knobPos)
	right := strings.Repeat("─", trackW-1-knobPos)
	track := m.sty.sDmr.Render(left) + knobPen.Render("●") + m.sty.sDmr.Render(right)

	// Value column: right-aligned within sliderValW cells.
	valStr := "—"
	if known {
		if sp.Min < 0 {
			valStr = toneStr(v)
		} else {
			valStr = strconv.Itoa(v)
		}
	}
	valPen := m.sty.sDim
	if focused {
		valPen = m.sty.sBri
	}
	vraw := Clip(valStr, sliderValW)
	vpad := sliderValW - DispW(vraw)
	valCell := strings.Repeat(" ", vpad) + valPen.Render(vraw)

	return labelCell + track + valCell
}

// eqSummary is the compact dashboard's one-line EQ readout. It runs in eqOrder so
// the display position matches the focus index, and — when the EQ pane has focus —
// highlights the selected band (accent + bold + underline; the underline keeps the
// cue legible even on a no-colour terminal), so a small screen still shows what
// ↑↓ will pick and ←→ will change. Parts are added until W is full (width-safe).
func (m *model) eqSummary(W int) string {
	_, vals := m.st.EQView()
	part := func(code string) string {
		sp, _ := tunnel.Lookup(code)
		v, known := vals[code]
		if !known {
			return eqShort[code] + " —"
		}
		if sp.Kind == tunnel.Toggle {
			st := "off"
			if v != 0 {
				st = "on"
			}
			return eqShort[code] + " " + st
		}
		if sp.Min < 0 {
			return string([]rune(eqShort[code])[:1]) + toneStr(v)
		}
		return fmt.Sprintf("%s %d", eqShort[code], v)
	}
	focusPen := m.sty.sAcc.Bold(true).Underline(true)
	sep := m.sty.sDmr.Render(" · ")
	var b strings.Builder
	used := 0
	for d, idx := range eqOrder {
		txt := part(tunnel.Specs[idx].Code)
		segW := DispW(txt)
		if d > 0 {
			segW += 3 // the " · " separator preceding every part but the first
		}
		if used+segW > W {
			break // out of room: stop cleanly rather than overflow the line
		}
		if d > 0 {
			b.WriteString(sep)
		}
		pen := m.sty.sDim
		if m.pane == paneEQ && m.eqFocus == d {
			pen = focusPen
		}
		b.WriteString(pen.Render(txt))
		used += segW
	}
	return b.String()
}

// toneStr formats a signed tone value: "+3", "0", "-6" (avoids an odd "+0").
func toneStr(v int) string {
	if v == 0 {
		return "0"
	}
	return fmt.Sprintf("%+d", v)
}
