// Rendering of the player: View's size dispatch and frame, the mini line, the
// full/compact dashboards, and their rows (header, now-playing metadata, seek,
// transport, volume rail, divider, footer).

package tui

import (
	"cmp"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/lucasdaddiego/lp10/internal/protocol"
)

func (m *model) View() string {
	if m.sty == nil {
		m.sty = newTheme()
	}
	rows, cols := m.rows, m.cols
	if rows == 0 || cols == 0 {
		return ""
	}
	s := m.st.Snap()
	m.motifLive, m.sonarLive = false, false         // set true below iff the plasma / sonar is actually drawn
	m.mzBtns, m.mzVol, m.mzEQ = nil, volZone{}, nil // cleared each frame; renderDashboard repopulates
	if rows < MiniRows || cols < MiniCols {
		m.diag = false
		return m.renderMini(s)
	}

	// The frame fills the whole terminal: W is the content width inside the
	// border (1+1) and padding (2+2); the renderers fill the body to the inner
	// height so the box touches all four window edges (no outer margin).
	W := cols - 6
	now := time.Now()

	var body string
	switch {
	case m.diag:
		body = m.renderDiag(s, now, W)
	default:
		full := rows >= FullRows && cols >= FullCols
		body = m.renderDashboard(s, now, W, full)
	}

	framed := lipgloss.NewStyle().
		Border(lipgloss.ThickBorder()).
		BorderForeground(m.sty.border).
		Padding(0, 2, 0, 2). // symmetric: content flush to the top and bottom borders, breathing room on the sides
		Render(body)
	return lipgloss.Place(cols, rows, lipgloss.Center, lipgloss.Center, framed)
}

func (m *model) renderMini(s protocol.Snapshot) string {
	t, cols := s.Track, m.cols
	switch {
	case s.Error != "" && (s.Fatal || time.Since(s.ErrorAt) < ErrorDisplayDuration):
		return stRed.Render(Clip(GL["warn"]+" "+friendlyError(s.Error), cols-1))
	case t != nil:
		glyph := GL["play"]
		if s.Playing != 0 {
			glyph = GL["pause"]
		}
		line := fmt.Sprintf("%s %s — %s  %s/%s  %d%%", glyph, t.Str("TrackName"), t.Str("Artist"),
			FmtMs(s.Pos), m.fmtRight(t.GetInt("TotalTime"), s.Pos), s.Vol)
		return m.sty.sTxt.Render(Clip(line, cols-1))
	default:
		msg := "connecting to LP10…"
		if s.Connected {
			msg = "nothing playing"
		}
		return m.sty.sDim.Render(Clip(GL["note"]+" "+msg, cols-1))
	}
}

func (m *model) renderDashboard(s protocol.Snapshot, now time.Time, W int, full bool) string {
	m.refreshAmbient(s) // recolour the meter/frame/dot to the cover (must precede headerRow)
	header := m.headerRow(s, now, W, full)
	// The bold-red error line is only for a fatal stop or a hiccup *while
	// connected*. A routine "can't reach the device" during reconnection is
	// already told by the header ("reconnecting…") and the idle reason below the
	// "connecting to LP10…" line, so don't also dump it red across the bottom.
	errLine := ""
	if s.Error != "" && (s.Fatal || (s.Connected && now.Sub(s.ErrorAt) < ErrorDisplayDuration)) {
		errLine = stRed.Render(Clip(GL["warn"]+" "+friendlyError(s.Error), W))
	}
	inner := m.rows - 2

	if full {
		// EQ: one horizontal row per band (W-wide), pinned to the bottom under a
		// divider. Build the tail first so the cover height is based on what's left.
		tail := append([]string{m.dividerRow("equalizer", W)}, m.eqSliders(s, W)...)
		tail = append(tail, m.footerRow(W))
		if errLine != "" {
			tail = append(tail, errLine)
		}
		// The framed cover fills the region between the header and that tail. Its
		// height comes from the real region; its width makes the box *square in
		// device pixels* using the measured cell aspect (cells are ~2:1, but the
		// exact ratio varies by font/terminal — assuming 2:1 left covers stretched).
		// Bounded by a hard height cap so it stays a cover, not a billboard, and by
		// width so the metadata column stays usable.
		// region minus the 2 frame rows, capped to a tasteful record sleeve (not a billboard)
		coverH := max(min((inner-2-len(tail))-2, coverHCap), 6)
		cellAR := 2.0 // cell height ÷ width; converts a cell count to display pixels
		if m.cellW > 0 && m.cellH > 0 {
			cellAR = float64(m.cellH) / float64(m.cellW)
		}
		// Size the box to the cover's TRUE aspect ratio (album art isn't always
		// square) so neither the half-block raster (which stretches to fill its cell
		// box) nor the Kitty placement distorts it: the box's display footprint
		// (coverW·cellW × coverH·cellH px) tracks the source's width:height. A square
		// cover keeps the old square box; a non-square one no longer gets stretched.
		srcAR := 1.0 // source width ÷ height
		if s.Art != nil {
			if b := s.Art.Bounds(); b.Dx() > 0 && b.Dy() > 0 {
				srcAR = float64(b.Dx()) / float64(b.Dy())
			}
		}
		coverW := int(float64(coverH)*cellAR*srcAR + 0.5)
		if maxW := W - 37; coverW > maxW { // reserve room for the metadata + volume columns
			coverW = maxW
			coverH = int(float64(coverW)/(cellAR*srcAR) + 0.5)
		}
		if coverH < 6 {
			coverH = 6
		}
		if coverW < 8 {
			coverW = 8
		}
		blockH := coverH + 2 // framed cover height = the now-playing block height
		midW := W - (coverW + 2) - volColW - 2*artGap
		// Three columns, all blockH tall: the framed album cover (left, a tidy sleeve);
		// the now-playing column (middle); and a full-height volume rail (right). The
		// middle is built as ONE cohesive block — title / artist / album, a blank, the
		// source·format line, a blank, then the seek bar + transport — and centred
		// vertically beside the cover. Grouping it tightly (rather than spreading the
		// pieces evenly down the whole height) keeps it ordered instead of scattered
		// with the source line floating in a void.
		mid := m.fullMeta(s, midW)
		if src := m.fullSourceLine(s, midW); src != "" {
			mid = append(mid, "", src)
		}
		mid = append(mid, "", m.seekRow(s, midW), "", m.transportSegments(s, now, midW))
		midLen := len(mid)
		mid = frameBody(mid, nil, blockH, true) // centre the cohesive block in the column
		art := m.boxArt(m.artColumn(s, coverW, coverH), coverW)
		colGap := strings.Repeat(" ", artGap)
		block := strings.Split(lipgloss.JoinHorizontal(lipgloss.Top,
			strings.Join(art, "\n"), colGap,
			strings.Join(mid, "\n"), colGap,
			strings.Join(m.volRail(s, blockH-1), "\n")), "\n")

		m.recordFullZones(coverW, midW, blockH, midLen, len(tail), inner, W)
		// header pinned top, EQ + footer pinned bottom, the cover block centred between
		return strings.Join(stack([]string{header, ""}, block, tail, inner), "\n")
	}

	// Compact: no art / vertical sliders — top-pinned metadata + seek + controls,
	// with the one-line EQ summary and footer pinned to the bottom.
	meta := m.metaLines(s, W)
	content := append([]string{header, ""}, meta...)
	content = append(content, "", m.seekRow(s, W), "", m.controlsRow(s, now, W, true))
	tail := []string{m.dividerRow("equalizer", W), m.eqSummary(W), m.footerRow(W)}
	if errLine != "" {
		tail = append(tail, errLine)
	}
	m.recordCompactZones(s, len(meta), len(tail), inner, W)
	return strings.Join(frameBody(content, tail, inner, false), "\n")
}

// stack composes exactly h lines: top pinned to the top, bottom pinned to the
// bottom, and middle vertically centred in the gap between. Callers size middle
// to fit the gap; any excess is trimmed from its bottom.
func stack(top, middle, bottom []string, h int) []string {
	if h <= 0 {
		return nil
	}
	out := make([]string, h)
	copy(out, top)
	copy(out[max(0, h-len(bottom)):], bottom)
	region := max(h-len(top)-len(bottom), 0)
	if len(middle) > region {
		middle = middle[:region]
	}
	copy(out[len(top)+(region-len(middle))/2:], middle)
	return out
}

// frameBody lays content and tail into exactly h lines so the bordered frame can
// span the full window height. The tail (footer / help / error) is always pinned
// to the bottom; the content is either top-aligned or vertically centred in the
// space above it (center). When content + tail overflow h, content is trimmed
// from the bottom so the tail stays visible (rather than letting Bubble Tea
// guillotine the top off-screen).
func frameBody(content, tail []string, h int, center bool) []string {
	if h <= 0 {
		return nil
	}
	if len(tail) >= h {
		return tail[len(tail)-h:]
	}
	room := h - len(tail)
	if len(content) > room {
		content = content[:room]
	}
	top := 0
	if center {
		top = (room - len(content)) / 2
	}
	out := make([]string, h) // zero value "" fills the gaps
	copy(out[top:], content)
	copy(out[room:], tail)
	return out
}

func (m *model) headerRow(s protocol.Snapshot, now time.Time, W int, full bool) string {
	clock := now.Format("15:04")
	note := GL["note"]

	// connection status sits on the left, next to the device name; in full mode
	// "Vol" labels the volume rail from the right, centred over its column so it
	// sits directly above the bar (which starts on the row just below).
	statTxt := "● " + clock // the green dot reads unambiguously as "connected"
	// The connected dot stays the theme's green — a status light, not an accent: an
	// album-tinted dot (e.g. orange for a sepia cover) reads as a warning. The
	// ambient hue still colours the seek bar and cover frame, just not this light.
	statStyled := m.sty.sAcc.Render("●") + m.sty.sDim.Render(" "+clock)
	if !s.Connected {
		statTxt = "● connecting…"
		if s.Attempts > 1 {
			statTxt = fmt.Sprintf("● reconnecting (%d)…", s.Attempts)
		}
		statStyled = stWarn.Render(statTxt)
	}

	prefixW := DispW(note) + 1 // "♪ "
	statW := DispW(statTxt)

	var vol string
	volW := 0
	if full {
		label := m.sty.sDim.Render("Vol")
		if s.Muted {
			label = stRed.Render("MUTED") // flag mute from the top, over the rail
		}
		vol = ccell(label, volColW)
		volW = volColW
	}

	// device name on the left; clip it so the status (and Vol) always fit, but
	// don't let a short name sprawl across a wide header.
	nameMax := max(min(W-prefixW-2-statW-volW-4, 24), 4)
	name := Clip(m.cfg.Name, nameMax)
	left := m.sty.sAcc.Render(note) + " " + m.sty.sAcc.Render(name) + "  " + statStyled
	leftW := prefixW + DispW(name) + 2 + statW

	// source/format fills the gap before Vol when a track is playing and there's
	// room; clipped to whatever space is left so the header never overflows W.
	right, rightW := vol, volW
	if q := sourceFormat(s.Track); q != "" {
		room := W - leftW - 1 // compact: one min gap before the right edge
		if full {
			room = W - leftW - volW - 3 // gap before quality + 2-col gap before Vol
		}
		if room >= 8 {
			var qStyled string
			var qW int
			if name := SourceName(s.Track); DispW(q) <= room && name != "" && strings.HasPrefix(q, name) {
				// fits fully: tint the source name in its brand colour, dim the format
				qStyled = sourceStyle(m.sty, name).Render(name) + m.sty.sDmr.Render(strings.TrimPrefix(q, name))
				qW = DispW(q)
			} else {
				c := Clip(q, room)
				qStyled, qW = m.sty.sDmr.Render(c), DispW(c)
			}
			if full {
				right, rightW = qStyled+"  "+vol, qW+2+volW
			} else {
				right, rightW = qStyled, qW
			}
		}
	}
	return between(left, leftW, right, rightW, W)
}

// Now-playing marquee tuning: a line wider than its column scrolls horizontally,
// looping with a gap and pausing briefly at the start so the head stays readable.
const (
	marqueeGap      = "      " // blank run between loop repetitions
	marqueeColTicks = 2        // ticks per one-column shift (~200ms at the 100ms tick)
	marqueePauseCol = 10       // columns of pause at the start of each loop
)

// marquee renders one now-playing line into width w: returned unchanged when it
// fits, otherwise a scrolling w-wide window that loops over time (driven by the
// model's tick counter, so all lines advance together).
func (m *model) marquee(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if DispW(s) <= w {
		return s
	}
	strip := s + marqueeGap
	stripW := DispW(strip)
	pos := (m.scroll / marqueeColTicks) % (stripW + marqueePauseCol)
	off := 0
	if pos > marqueePauseCol {
		off = pos - marqueePauseCol
	}
	return dispWindow(strip+strip, off, w)
}

// metaLines renders the now-playing text: title, artist · album, and the
// technical format line (or a connecting/idle message). The track lines scroll
// as a marquee when they overflow w; the idle messages are clipped.
func (m *model) metaLines(s protocol.Snapshot, w int) []string {
	t := s.Track
	if t == nil {
		msg := "connecting to LP10…"
		if s.Connected {
			msg = "nothing playing"
		}
		out := []string{m.sty.sDim.Render(Clip(msg, w))}
		switch {
		case s.Connected:
			out = append(out, m.sty.sDmr.Render(Clip("start something on Spotify / AirPlay / BT", w)))
		case s.Error != "":
			// disconnected: a calm reason under "connecting…", not a red bottom line
			out = append(out, m.sty.sDmr.Render(Clip(friendlyError(s.Error), w)))
		}
		return out
	}
	name := cmp.Or(t.Str("TrackName"), "—")
	second := t.Str("Artist")
	if al := t.Str("Album"); al != "" {
		if second != "" {
			second += " · " + al
		} else {
			second = al
		}
	}
	// Make the title and artist clickable (OSC 8) where the terminal supports
	// it — a degrades-to-plain enhancement, so it's always on. The link wraps
	// the fully styled+marqueed line so no later width math (DispW) ever sees
	// the URL bytes; downstream layout measures via lipgloss, which ignores it.
	// The source/format ("Spotify · Ogg · 44.1 kHz") rides the header row, not
	// here, so the now-playing block stays a tight two lines.
	artist := t.Str("Artist")
	trackLink := spotifySearch(strings.TrimSpace(name + " " + artist))
	secondLink := cmp.Or(spotifySearch(artist), spotifySearch(t.Str("Album")))
	return []string{
		osc8(trackLink, m.sty.sBri.Render(m.marquee(name, w))),
		osc8(secondLink, m.sty.sDim.Render(m.marquee(second, w))),
	}
}

// sourceStyle tints a source name in its brand colour (a small, tasteful accent
// in the otherwise teal/grey header), falling back to the theme accent.
func sourceStyle(t *theme, name string) lipgloss.Style {
	fg := func(hex string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(hex)) }
	switch name {
	case "Spotify":
		return fg("#1db954")
	case "TIDAL":
		return fg("#4fd4d4")
	case "AirPlay":
		return fg("#cfd6df")
	case "Bluetooth":
		return fg("#4a90d9")
	default:
		return t.sAcc
	}
}

// sourceFormat is the "Source · Mime · NN kHz" descriptor for a track (e.g.
// "Spotify · Ogg · 44.1 kHz"), or "" when nothing is playing.
func sourceFormat(t protocol.Track) string {
	if t == nil {
		return ""
	}
	var q []string
	if sn := SourceName(t); sn != "" {
		q = append(q, sn)
	}
	if ql := Quality(t); ql != "" {
		q = append(q, ql)
	}
	return strings.Join(q, " · ")
}

// osc8 wraps text in an OSC 8 hyperlink to url. Terminals that support
// hyperlinks (Ghostty, kitty, iTerm2, modern VTE) make the text clickable;
// others ignore the escape and show the text verbatim. The sequence is zero
// display-width to lipgloss/x-ansi, but NOT to DispW (which counts the URL
// bytes), so only ever apply it at the outermost layer, past all width math.
func osc8(url, text string) string {
	if url == "" {
		return text
	}
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// spotifySearch builds an open.spotify.com search URL for query, or "" when the
// query is empty. Robust across sources (works for AirPlay/Bluetooth tracks too,
// where there's no Spotify URI), at the cost of landing on a search rather than
// the exact track.
func spotifySearch(query string) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}
	return "https://open.spotify.com/search/" + url.PathEscape(query)
}

// transportSegments renders prev / play-pause / next as three equal-width (~33%)
// filled segments spanning width w, each with its label centred. Falls back to
// the volume-entry prompt while it's active.
func (m *model) transportSegments(s protocol.Snapshot, now time.Time, w int) string {
	segs := []struct{ action, label string }{
		{"prev", GL["rew"]}, {"toggle", toggleVerb(s)}, {"next", GL["ff"]},
	}
	pad, widths, gap := transportLayout(w)
	var b strings.Builder
	cluster := 0
	for i, sg := range segs {
		if i > 0 {
			b.WriteString(strings.Repeat(" ", gap)) // horizontal gap between buttons
			cluster += gap
		}
		st := m.sty.segOff
		if (m.pane == paneNow && sg.action == actions[m.focus]) || m.flash[sg.action].After(now) {
			st = m.sty.segOn
		}
		cw := widths[i]
		lab := Clip(sg.label, cw)
		lw := DispW(lab)
		lp := (cw - lw) / 2
		b.WriteString(st.Render(strings.Repeat(" ", lp) + lab + strings.Repeat(" ", cw-lw-lp)))
		cluster += cw
	}
	return strings.Repeat(" ", pad) + b.String() + strings.Repeat(" ", w-cluster-pad)
}

// transportLayout returns the leading pad, the three segment widths, and the gap
// between buttons for the transport cluster in a w-wide column. The buttons are a
// tidy centred cluster (capped, with leftover width padded on either side), and a
// small gap separates them so they read as three distinct buttons rather than one
// connected bar. Shared by transportSegments (rendering) and the mouse hit-zone
// builder so the two never disagree.
func transportLayout(w int) (pad int, widths []int, gap int) {
	const maxCluster = 52
	gap = transportGap
	cluster := min(w, maxCluster)
	btnTotal := cluster - gap*(len(actions)-1)
	if btnTotal < len(actions) { // too narrow for gaps: fall back to a solid cluster
		btnTotal, gap = cluster, 0
	}
	return (w - cluster) / 2, splitWidth(btnTotal, len(actions)), gap
}

// transportGap is the blank columns between transport buttons (horizontal separation).
const transportGap = 2

const (
	volColW   = 7  // width of the volume rail column
	artGap    = 2  // blank columns between the three player columns (art | mid | vol)
	coverHCap = 16 // max album-cover height (rows): a record sleeve, not a billboard
)

// fullMeta is the now-playing metadata for the full dashboard: title, artist, and
// album each on their own line (clickable + marqueed), so the smaller cover's freed
// width reads as a card. Falls back to metaLines' idle/connecting copy when nothing
// is playing. The compact view keeps metaLines' tighter two-line form.
func (m *model) fullMeta(s protocol.Snapshot, w int) []string {
	t := s.Track
	if t == nil {
		return m.metaLines(s, w)
	}
	name := cmp.Or(t.Str("TrackName"), "—")
	artist := t.Str("Artist")
	out := []string{osc8(spotifySearch(strings.TrimSpace(name+" "+artist)),
		m.sty.sBri.Render(m.marquee(name, w)))}
	if artist != "" {
		out = append(out, osc8(spotifySearch(artist), m.sty.sDim.Render(m.marquee(artist, w))))
	}
	if album := t.Str("Album"); album != "" {
		out = append(out, osc8(spotifySearch(album), m.sty.sDmr.Render(m.marquee(album, w))))
	}
	return out
}

// fullSourceLine is the prominent source/format line in the full player: a
// brand-tinted dot + "Spotify · Ogg · 44.1 kHz · 2 ch". The source name wears its
// brand colour, the rest is dim. Returns "" when nothing is playing or there's no
// format to show; degrades to a dim clip when it can't fit w.
func (m *model) fullSourceLine(s protocol.Snapshot, w int) string {
	t := s.Track
	if t == nil {
		return ""
	}
	q := sourceFormat(t)
	if q == "" {
		return ""
	}
	if ch := t.GetInt("ChannelCount"); ch > 0 {
		q += fmt.Sprintf(" · %d ch", ch)
	}
	plain := "● " + q
	if DispW(plain) > w { // too narrow: a plain dim clip keeps the width contract
		return m.sty.sDmr.Render(Clip(plain, w))
	}
	pen := sourceStyle(m.sty, SourceName(t))
	body := m.sty.sDmr.Render(q)
	if name := SourceName(t); name != "" && strings.HasPrefix(q, name) {
		body = pen.Render(name) + m.sty.sDmr.Render(strings.TrimPrefix(q, name))
	}
	return pen.Render("●") + " " + body
}

// volRail renders the volume like an EQ band: a vertical bar barH squares tall
// with the value (percentage, or "muted") centred on the row below it. "Vol"
// labels it from the header; the m key toggles mute. Returns barH+1 lines.
func (m *model) volRail(s protocol.Snapshot, barH int) []string {
	rows := make([]string, 0, barH+1)
	if s.Muted {
		// Impossible to miss: a SOLID red column (not a faint hollow one that reads
		// as "volume happens to be 0") under a bold red MUTED badge. The header's
		// "Vol" label also flips to a red "MUTED" so it's caught from the top too.
		col := stRed.Render("█")
		for range barH {
			rows = append(rows, ccell(col, volColW))
		}
		return append(rows, ccell(stRed.Render("MUTED"), volColW))
	}
	for _, bl := range m.sty.vbar(float64(s.Vol)/100, barH) {
		rows = append(rows, ccell(bl, volColW))
	}
	return append(rows, ccell(m.sty.sDim.Render(fmt.Sprintf("%d%%", s.Vol)), volColW))
}

func (m *model) seekRow(s protocol.Snapshot, W int) string {
	t := s.Track
	playing := s.Playing == 0 && t != nil

	// A colour-coded STATE label owns play/pause prominence: a teal "Playing" while
	// playing, an amber "Paused" when paused. The transport toggle button is an
	// icon-free verb (play/pause), so the state indicator and the action label never
	// duel. Padded to a fixed width so the meter's start column doesn't jump on a
	// state change.
	const statusW = 9 // DispW("▶ Playing")
	var status string
	switch {
	case playing:
		status = m.sty.sAcc.Bold(true).Render(padDisp(GL["play"]+" Playing", statusW))
	case t != nil:
		status = stWarn.Bold(true).Render(padDisp(GL["pause"]+" Paused", statusW))
	default:
		status = m.sty.sDmr.Render(padDisp(GL["pause"], statusW)) // idle: a quiet marker
	}

	total, pos := 0, s.Pos
	if t != nil {
		total = t.GetInt("TotalTime")
	} else {
		pos = 0 // nothing playing: don't bleed a stale elapsed time into the idle bar
	}
	cur := FmtMs(pos)
	rem := m.fmtRight(total, pos)
	cells := max(W-(statusW+1+DispW(cur)+1+1+DispW(rem)), 1)
	frac := 0.0
	if total > 0 {
		frac = float64(pos) / float64(total)
	}
	fill, head := m.sty.fill, m.sty.head
	if m.amb != nil {
		fill, head = m.amb.fill, m.amb.head // the seek bar wears the album's colour
	}
	return status + " " + m.sty.sDim.Render(cur) + " " +
		m.sty.lineMeterPen(frac, cells, fill, head) + " " + m.sty.sDim.Render(rem)
}

func (m *model) controlsRow(s protocol.Snapshot, now time.Time, W int, withVol bool) string {
	btn := func(action, label string) (string, int) {
		st := m.sty.btnOff
		if (m.pane == paneNow && action == actions[m.focus]) || m.flash[action].After(now) {
			st = m.sty.btnOn
		}
		return st.Render(label), DispW(label) + 2
	}
	pv, pvW := btn("prev", GL["rew"])
	tg, tgW := btn("toggle", toggleVerb(s))
	nx, nxW := btn("next", GL["ff"])
	left := pv + " " + tg + " " + nx
	leftW := pvW + 1 + tgW + 1 + nxW
	if !withVol {
		return left // volume is shown as a vertical band in the now-playing block
	}

	muteLbl := "mute"
	if s.Muted {
		muteLbl = "unmute"
	}
	volCells := 10
	volVal := fmt.Sprintf("%d%%", s.Vol)
	volPen, volLabel := m.sty.sBri, m.sty.sDmr
	if s.Muted {
		volVal, volPen = "MUTED", stRed
	}
	mt, mtW := btn("mute", muteLbl)
	right := volLabel.Render("vol") + " " + m.sty.lineMeter(float64(s.Vol)/100, volCells) + " " +
		volPen.Render(volVal) + "  " + mt
	rightW := 3 + 1 + volCells + 1 + DispW(volVal) + 2 + mtW

	return between(left, leftW, right, rightW, W)
}

// dividerRow is a section separator: the label centred between two dim rules,
// "──── label ────", W cells wide, so the title reads as a heading.
func (m *model) dividerRow(label string, W int) string {
	rule := max(W-DispW(label)-2, 0) // a space flanks the label on each side
	left := rule / 2
	bar := func(n int) string { return m.sty.sDmr.Render(strings.Repeat(GL["track"], n)) }
	return bar(left) + " " + m.sty.sDim.Render(label) + " " + bar(rule-left)
}

func (m *model) footerRow(W int) string {
	var hint string
	switch {
	case m.pane == paneEQ && m.eqSpec().Code == "MXV":
		// The one band with a device-wide gotcha (teardown §5.3): a low output cap
		// is why the remote / Spotify volume feels stuck near the top.
		hint = "Max Vol caps remote & Spotify volume · ←→ adjust · q quit"
	case m.pane == paneEQ:
		hint = "↑↓ pick · ←→ adjust · enter toggle · tab player · q quit"
	default:
		hint = "space play · ↑↓ vol · m mute · e/tab EQ · ? diag · q quit"
	}
	return lipgloss.NewStyle().Width(W).Align(lipgloss.Right).
		Render(m.sty.sDmr.Render(Clip(hint, W)))
}

// toggleVerb is the transport toggle's icon-free action label: "pause" while
// playing (press to pause), "play" while paused or idle (press to play). It carries
// no play/pause glyph so it never duels with the colour-coded STATE shown on the
// seek row. Shared by transportSegments, controlsRow, and recordCompactZones so the
// rendered button and its hit-zone width never disagree.
func toggleVerb(s protocol.Snapshot) string {
	if s.Playing == 0 {
		return "pause"
	}
	return "play"
}
