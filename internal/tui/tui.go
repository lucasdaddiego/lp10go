package tui

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lucasdaddiego/lp10go/internal/config"
	"github.com/lucasdaddiego/lp10go/internal/protocol"
	"github.com/lucasdaddiego/lp10go/internal/tunnel"
	"github.com/lucasdaddiego/lp10go/internal/workers"
)

// Display/timing constants.
const (
	FlashDuration        = 350 * time.Millisecond
	ErrorDisplayDuration = 4 * time.Second
	MaxTitleLength       = 120

	// StatsReassertTicks re-sends the "stats on" signal while the diagnostics
	// overlay is open (30 ticks × 100ms ≈ 3s), so the device — which resets the
	// flag on every reconnect — resumes emitting @@s within a few seconds of a
	// dropped/restored connection. Kept under CommandDeadline so it stays fresh.
	StatsReassertTicks = 30

	// Layout thresholds (rows × cols). Below mini -> one frameless line; below
	// the full size -> a compact dashboard with no art and a one-line EQ.
	MiniRows = 9
	MiniCols = 58
	FullRows = 25 // full dashboard (art + graphic EQ + volume slider) needs the height
	FullCols = 70 // 7 EQ bands need ≥9 cols each so "Deep Bass" fits its column
)

// actions is the focusable transport-button order in the now-playing pane.
var actions = []string{"prev", "toggle", "next"}

// eqOrder maps EQ-strip display position -> index into tunnel.Specs, so the
// graphic equalizer reads EQ · Treble · Mid · Bass · Sub · Lvl · Max Vol while the
// wire-level Specs order is unchanged. Max Vol (the rarely-touched output cap) sits
// last.
var eqOrder = []int{1, 4, 3, 2, 5, 6, 0}

// eqShort is the compact band label per wire code.
var eqShort = map[string]string{"MXV": "Max Vol", "EQS": "EQ", "TRE": "Treble", "MID": "Mid", "BAS": "Bass", "VBS": "Sub", "VBI": "Lvl"}

var (
	stWarn = lipgloss.NewStyle().Foreground(lipgloss.Color("#e0b34d"))
	stRed  = lipgloss.NewStyle().Foreground(lipgloss.Color("#e2655f")).Bold(true)
)

// pane identifiers (which half of the dashboard has focus).
const (
	paneNow = 0 // now-playing transport
	paneEQ  = 1 // equalizer strip
)

// model is the Bubble Tea model: controller logic plus render state.
type model struct {
	st     *protocol.State
	cfg    config.Config
	cmds   chan *protocol.Command
	eqcmds chan workers.EQCommand

	focus         int // transport-button focus (index into actions)
	pane          int // paneNow | paneEQ
	eqFocus       int // EQ-strip display position (index into eqOrder)
	frame         int // animation frame for the art motif (advances while playing)
	diag          bool
	showRemaining bool
	flash         map[string]time.Time

	rows, cols int
	curTitle   string

	// statsOn tracks whether the device has been told to emit resource stats
	// (@@s), so it runs only while the diagnostics overlay is open; statsTicks
	// counts down to the next keep-alive re-assert.
	statsOn    bool
	statsTicks int

	// motif cache: the plasma is a pure function of (w,h,frame), so a frozen
	// frame (paused/idle) or any non-tick re-render reuses the last block
	// instead of rebuilding 72 styled cells ~10x/sec. Byte-identical output.
	motifBlk []string
	motifKey [3]int // w, h, frame the cache was built for

	interrupted bool // Ctrl-C, so Run can exit 130 like Python's KeyboardInterrupt

	sty *theme
}

// motif returns the cached art block, recomputing only when (w,h,frame) changes.
func (m *model) motif(w, h int) []string {
	key := [3]int{w, h, m.frame}
	if m.motifBlk == nil || m.motifKey != key {
		m.motifBlk = m.sty.motifBlock(w, h, m.frame)
		m.motifKey = key
	}
	return m.motifBlk
}

func newModel(st *protocol.State, cfg config.Config, cmds chan *protocol.Command, eqcmds chan workers.EQCommand) *model {
	return &model{
		st: st, cfg: cfg, cmds: cmds, eqcmds: eqcmds,
		focus:         1,
		showRemaining: true,
		flash:         map[string]time.Time{},
	}
}

type tickMsg struct{}

func tick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *model) Init() tea.Cmd { return tick() }

// send enqueues a transport command without ever blocking the update loop
// (drop-oldest on a full buffer; commands are coalesced/aged-out downstream).
func (m *model) send(mid int, data string) {
	cmd := &protocol.Command{Mid: mid, Data: data, TS: time.Now()}
	select {
	case m.cmds <- cmd:
	default:
		select {
		case <-m.cmds:
		default:
		}
		select {
		case m.cmds <- cmd:
		default:
		}
	}
}

// syncStats keeps the device's resource-stat (@@s) emission aligned with the
// diagnostics overlay: send "on" (90 1) when it opens and re-assert every
// StatsReassertTicks so a reconnect resumes it; send "off" (90 0) once when it
// closes (by any path, including a resize to the mini view). Off the overlay the
// box does no /proc gathering at all.
func (m *model) syncStats() {
	switch {
	case m.diag && (!m.statsOn || m.statsTicks <= 0):
		m.send(90, "1")
		m.statsOn = true
		m.statsTicks = StatsReassertTicks
	case m.diag:
		m.statsTicks--
	case m.statsOn:
		m.send(90, "0")
		m.statsOn = false
		m.statsTicks = 0
	}
}

func (m *model) setVol(v int) { m.send(64, strconv.Itoa(m.st.SetVol(v))) }

func (m *model) do(action string) {
	m.flash[action] = time.Now().Add(FlashDuration)
	switch action {
	case "toggle":
		if m.st.ToggleOptimistic() {
			m.send(40, "PAUSE")
		} else {
			m.send(40, "RESUME")
		}
	case "next":
		m.send(40, "NEXT")
	case "prev":
		m.send(40, "PREV")
	case "volup":
		m.send(64, strconv.Itoa(m.st.AdjustVol(+m.cfg.VolStep)))
	case "voldn":
		m.send(64, strconv.Itoa(m.st.AdjustVol(-m.cfg.VolStep)))
	case "mute":
		vol, premute := m.st.VolAndPremute()
		if vol > 0 {
			m.setVol(0)
		} else {
			if premute == 0 {
				premute = config.LoadPremute(m.st.PremuteFile)
			}
			m.setVol(premute)
		}
	}
}

// ---- EQ controls (the :2018 tunnel) ----

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
	cmd := workers.EQCommand{Code: code, Val: val}
	select {
	case m.eqcmds <- cmd:
	default:
		select {
		case <-m.eqcmds:
		default:
		}
		select {
		case m.eqcmds <- cmd:
		default:
		}
	}
}

// keyKind is the normalized key class dispatched to the controller logic.
type keyKind int

const (
	kOther keyKind = iota
	kEnter
	kEsc
	kBackspace
	kLeft
	kRight
	kUp
	kDown
	kTab
	kShiftTab
	kRune
)

type keyEvent struct {
	kind keyKind
	r    rune
}

func translate(msg tea.KeyMsg) keyEvent {
	switch msg.Type {
	case tea.KeyEnter:
		return keyEvent{kind: kEnter}
	case tea.KeyEsc:
		return keyEvent{kind: kEsc}
	case tea.KeyBackspace:
		return keyEvent{kind: kBackspace}
	case tea.KeyLeft:
		return keyEvent{kind: kLeft}
	case tea.KeyRight:
		return keyEvent{kind: kRight}
	case tea.KeyUp:
		return keyEvent{kind: kUp}
	case tea.KeyDown:
		return keyEvent{kind: kDown}
	case tea.KeyTab:
		return keyEvent{kind: kTab}
	case tea.KeyShiftTab:
		return keyEvent{kind: kShiftTab}
	case tea.KeySpace:
		return keyEvent{kind: kRune, r: ' '}
	case tea.KeyRunes:
		if len(msg.Runes) == 1 {
			return keyEvent{kind: kRune, r: msg.Runes[0]}
		}
	}
	return keyEvent{kind: kOther}
}

// translateAll expands one key message into the events to dispatch. Bubble Tea
// coalesces a run of consecutive printable runes (an unbracketed paste, fast
// typing, or scripted `tmux send-keys`) into a single KeyRunes carrying several
// runes; each must be dispatched in order, or the whole batch is silently lost.
func translateAll(msg tea.KeyMsg) []keyEvent {
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 {
		evs := make([]keyEvent, len(msg.Runes))
		for i, r := range msg.Runes {
			evs[i] = keyEvent{kind: kRune, r: r}
		}
		return evs
	}
	return []keyEvent{translate(msg)}
}

// key dispatches one keypress, returning "quit", "drain" (bare ESC), or "".
func (m *model) key(ev keyEvent) string {
	if m.diag {
		m.diag = false // any key closes the overlay
		return ""
	}

	// tab toggles which pane has focus.
	if ev.kind == kTab || ev.kind == kShiftTab {
		m.pane = (m.pane + 1) % 2
		return ""
	}
	if ev.kind == kEsc {
		if m.pane == paneEQ {
			m.pane = paneNow // step back to the player, don't quit
			return ""
		}
		return "drain"
	}
	if ev.kind == kRune && (ev.r == 'q' || ev.r == 'Q') {
		return "quit"
	}

	// directional keys are pane-specific.
	switch ev.kind {
	case kUp:
		if m.pane == paneEQ {
			m.eqAdjust(+1) // raise the focused band's value
		} else {
			m.do("volup")
		}
		return ""
	case kDown:
		if m.pane == paneEQ {
			m.eqAdjust(-1) // lower the focused band's value
		} else {
			m.do("voldn")
		}
		return ""
	case kLeft:
		if m.pane == paneEQ {
			m.eqFocus = (m.eqFocus - 1 + len(eqOrder)) % len(eqOrder) // select band left
		} else {
			m.focus = (m.focus - 1 + len(actions)) % len(actions)
		}
		return ""
	case kRight:
		if m.pane == paneEQ {
			m.eqFocus = (m.eqFocus + 1) % len(eqOrder) // select band right
		} else {
			m.focus = (m.focus + 1) % len(actions)
		}
		return ""
	case kEnter:
		if m.pane == paneEQ {
			m.eqToggleFocused()
		} else {
			m.do(actions[m.focus])
		}
		return ""
	}

	// playback / global rune keys work regardless of pane.
	if ev.kind == kRune {
		switch ev.r {
		case ' ':
			m.do("toggle")
		case 'n':
			m.do("next")
		case 'p':
			m.do("prev")
		case '+', '=':
			m.do("volup")
		case '-', '_':
			m.do("voldn")
		case 'm':
			m.do("mute")
		case 't':
			m.showRemaining = !m.showRemaining
		case 'e':
			m.pane = paneEQ
		case '?':
			m.diag = true
		}
	}
	return ""
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.rows, m.cols = msg.Height, msg.Width
		return m, nil
	case tickMsg:
		cmds := []tea.Cmd{tick()}
		s := m.st.Snap() // one snapshot per tick, reused below
		if s.Playing == 0 {
			m.frame++ // advance the art motif only while playing
		}
		m.syncStats() // device emits @@s only while the diag overlay is open
		if title := m.computeTitle(s); title != m.curTitle {
			m.curTitle = title
			cmds = append(cmds, tea.SetWindowTitle(title))
		}
		return m, tea.Batch(cmds...)
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			m.interrupted = true
			return m, tea.Quit
		}
		for _, ev := range translateAll(msg) {
			if m.key(ev) == "quit" {
				return m, tea.Quit
			}
		}
		return m, nil
	}
	return m, nil
}

func (m *model) computeTitle(s protocol.Snapshot) string {
	var text string
	if s.Track != nil {
		text = GL["note"] + " " + s.Track.Str("TrackName") + " — " + s.Track.Str("Artist")
	} else {
		text = m.cfg.Name
	}
	var b strings.Builder
	n := 0
	for _, r := range text {
		if n >= MaxTitleLength {
			break
		}
		if unicode.IsPrint(r) {
			b.WriteRune(r)
			n++
		}
	}
	return b.String()
}

func (m *model) fmtRight(total, pos int) string {
	if m.showRemaining && total > 0 {
		return "-" + FmtMs(total-pos)
	}
	return FmtMs(total)
}

// ---- rendering ---------------------------------------------------------------

func (m *model) View() string {
	if m.sty == nil {
		m.sty = newTheme()
	}
	rows, cols := m.rows, m.cols
	if rows == 0 || cols == 0 {
		return ""
	}
	s := m.st.Snap()
	if rows < MiniRows || cols < MiniCols {
		m.diag = false
		return m.renderMini(s)
	}

	W := cols - 6 // inside border (1+1) and padding (2+2)
	if W > 96 {
		W = 96
	}
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
		Padding(1, 2, 0, 2). // bottom=0 so the help line hugs the bottom border
		Render(body)
	return lipgloss.Place(cols, rows, lipgloss.Center, lipgloss.Center, framed)
}

func (m *model) renderMini(s protocol.Snapshot) string {
	t, cols := s.Track, m.cols
	switch {
	case s.Error != "" && (s.Fatal || time.Since(s.ErrorAt) < ErrorDisplayDuration):
		return stRed.Render(Clip(GL["warn"]+" "+s.Error, cols-1))
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
	var rowsOut []string
	add := func(lines ...string) { rowsOut = append(rowsOut, lines...) }
	gap := func() { add("") }

	ctrlRow := func(w int) string {
		return m.controlsRow(s, now, w, !full)
	}

	add(m.headerRow(s, now, W, full))
	gap()
	if full {
		eb := m.ebHeight()
		railH := eb + 1              // bar + value row — the same shape as an EQ band
		midW := W - 12 - volColW - 4 // art(12) + 2 gaps + Vol rail + 2 gaps
		// Three columns: album-motif art (left); a middle column with the metadata
		// then — after a breathing line — the timeline and segmented transport
		// buttons; and the volume rail (right): a vertical bar with its value
		// below, exactly like an EQ band. The blank middle line makes all three
		// columns railH tall, so the value lands on the buttons row instead of
		// floating a row beneath everything else.
		mid := m.metaLines(s, midW)
		mid = append(mid, "", m.seekRow(s, midW), m.transportSegments(s, now, midW))
		art := m.motif(12, railH)
		player := lipgloss.JoinHorizontal(lipgloss.Top,
			strings.Join(art, "\n"), "  ",
			strings.Join(mid, "\n"), "  ",
			strings.Join(m.volRail(s, eb), "\n"))
		add(strings.Split(player, "\n")...)
		gap()
		add(m.dividerRow("equalizer", W))
		gap()
		add(m.eqStrip(s, W, eb)...)
	} else {
		add(m.metaLines(s, W)...)
		gap()
		add(m.seekRow(s, W))
		gap()
		add(ctrlRow(W))
		gap()
		add(m.dividerRow("equalizer", W))
		add(m.eqSummary(W))
		gap()
	}
	add(m.footerRow(W))

	if s.Error != "" && (s.Fatal || now.Sub(s.ErrorAt) < ErrorDisplayDuration) {
		add(stRed.Render(Clip(GL["warn"]+" "+s.Error, W)))
	}

	// Never overflow the frame: cap the body to the inner height (ThickBorder
	// top+bottom + top padding = 3 rows of chrome). When the body fits — the
	// normal case — this is a no-op and the dashboard renders byte-identically;
	// on a too-short pane it trims from the bottom rather than letting Bubble
	// Tea guillotine the top border + header off the top.
	if budget := m.rows - 3; budget > 0 && len(rowsOut) > budget {
		rowsOut = rowsOut[:budget]
	}
	return strings.Join(rowsOut, "\n")
}

// ebHeight is the shared slider height — 5 squares for the EQ bands and the
// volume bar, so they look identical (five steps suit the tone controls' -10..+10
// range). Each slider adds a label row above and a value row below the bar.
func (m *model) ebHeight() int { return 5 }

func (m *model) headerRow(s protocol.Snapshot, now time.Time, W int, full bool) string {
	clock := now.Format("15:04")
	note := GL["note"]

	// connection status sits on the left, next to the device name; in full mode
	// "Vol" labels the volume rail from the right, centred over its column so it
	// sits directly above the bar (which starts on the row just below).
	statTxt := "● connected · " + clock
	statStyled := m.sty.sAcc.Render("●") + m.sty.sDim.Render(" connected · "+clock)
	if !s.Connected {
		statTxt = "● connecting…"
		if s.Attempts > 1 {
			statTxt = fmt.Sprintf("● reconnecting (%d)…", s.Attempts)
		}
		statStyled = stWarn.Render(statTxt)
	}

	var right string
	var rightW int
	if full {
		right = ccell(m.sty.sDim.Render("Vol"), volColW)
		rightW = volColW
	}

	prefixW := DispW(note) + 1 // "♪ "
	maxName := W - rightW - 1 - prefixW - 2 - DispW(statTxt)
	if maxName < 4 {
		maxName = 4
	}
	name := Clip(m.cfg.Name, maxName)
	left := m.sty.sAcc.Render(note) + " " + m.sty.sAcc.Render(name) + "  " + statStyled
	leftW := prefixW + DispW(name) + 2 + DispW(statTxt)
	return between(left, leftW, right, rightW, W)
}

// metaLines renders the now-playing text: title, artist · album, and the
// technical format line (or a connecting/idle message), each clipped to w.
func (m *model) metaLines(s protocol.Snapshot, w int) []string {
	t := s.Track
	if t == nil {
		msg := "connecting to LP10…"
		if s.Connected {
			msg = "nothing playing"
		}
		out := []string{m.sty.sDim.Render(Clip(msg, w))}
		if s.Connected {
			out = append(out, m.sty.sDmr.Render(Clip("start something on Spotify / AirPlay / BT", w)))
		}
		return out
	}
	name := t.Str("TrackName")
	if name == "" {
		name = "—"
	}
	second := t.Str("Artist")
	if al := t.Str("Album"); al != "" {
		if second != "" {
			second += " · " + al
		} else {
			second = al
		}
	}
	var q []string
	if sn := SourceName(t); sn != "" {
		q = append(q, sn)
	}
	if ql := Quality(t); ql != "" {
		q = append(q, ql)
	}
	return []string{
		m.sty.sBri.Render(Clip(name, w)),
		m.sty.sDim.Render(Clip(second, w)),
		m.sty.sDmr.Render(Clip(strings.Join(q, " · "), w)),
	}
}

// transportSegments renders prev / play-pause / next as three equal-width (~33%)
// filled segments spanning width w, each with its label centred. Falls back to
// the volume-entry prompt while it's active.
func (m *model) transportSegments(s protocol.Snapshot, now time.Time, w int) string {
	toggleLbl := "▶ play"
	if s.Playing == 0 {
		toggleLbl = "⏸ pause"
	}
	segs := []struct{ action, label string }{
		{"prev", GL["rew"]}, {"toggle", toggleLbl}, {"next", GL["ff"]},
	}
	widths := splitWidth(w, len(segs))
	var b strings.Builder
	for i, sg := range segs {
		st := m.sty.segOff
		if (m.pane == paneNow && sg.action == actions[m.focus]) || m.flash[sg.action].After(now) {
			st = m.sty.segOn
		}
		cw := widths[i]
		lab := Clip(sg.label, cw)
		lw := DispW(lab)
		lp := (cw - lw) / 2
		b.WriteString(st.Render(strings.Repeat(" ", lp) + lab + strings.Repeat(" ", cw-lw-lp)))
	}
	return b.String()
}

const volColW = 7

// ccell centres an already-styled string in a colW-wide block via lipgloss
// (display-width aware, ANSI-safe), so labels, bars, and values of differing
// widths line up identically — the volume rail and the EQ bands lean on it.
func ccell(s string, colW int) string {
	return lipgloss.NewStyle().Width(colW).Align(lipgloss.Center).Render(s)
}

// volRail renders the volume like an EQ band: a vertical bar barH squares tall
// with the value (percentage, or "muted") centred on the row below it. "Vol"
// labels it from the header; the m key toggles mute. Returns barH+1 lines.
func (m *model) volRail(s protocol.Snapshot, barH int) []string {
	rows := make([]string, 0, barH+1)
	for _, bl := range m.sty.vbar(float64(s.Vol)/100, barH) {
		rows = append(rows, ccell(bl, volColW))
	}
	valTxt, valPen := fmt.Sprintf("%d%%", s.Vol), m.sty.sDim
	if s.Muted {
		valTxt, valPen = "muted", stRed
	}
	return append(rows, ccell(valPen.Render(valTxt), volColW))
}

func (m *model) seekRow(s protocol.Snapshot, W int) string {
	t := s.Track
	playing := s.Playing == 0 && t != nil
	glyph := GL["play"]
	if !playing {
		glyph = GL["pause"]
	}
	total := 0
	if t != nil {
		total = t.GetInt("TotalTime")
	}
	cur := FmtMs(s.Pos)
	rem := m.fmtRight(total, s.Pos)
	cells := W - (1 + 1 + DispW(cur) + 1 + 1 + DispW(rem))
	if cells < 1 {
		cells = 1
	}
	frac := 0.0
	if total > 0 {
		frac = float64(s.Pos) / float64(total)
	}
	return m.sty.sAcc.Render(glyph) + " " + m.sty.sDim.Render(cur) + " " +
		m.sty.lineMeter(frac, cells) + " " + m.sty.sDim.Render(rem)
}

func (m *model) controlsRow(s protocol.Snapshot, now time.Time, W int, withVol bool) string {
	playing := s.Playing == 0
	toggleLbl := "▶ play"
	if playing {
		toggleLbl = "⏸ pause"
	}
	btn := func(action, label string) (string, int) {
		st := m.sty.btnOff
		if (m.pane == paneNow && action == actions[m.focus]) || m.flash[action].After(now) {
			st = m.sty.btnOn
		}
		return st.Render(label), DispW(label) + 2
	}
	pv, pvW := btn("prev", GL["rew"])
	tg, tgW := btn("toggle", toggleLbl)
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
	mt, mtW := btn("mute", muteLbl)
	right := volLabel.Render("vol") + " " + m.sty.lineMeter(float64(s.Vol)/100, volCells) + " " +
		volPen.Render(volVal) + "  " + mt
	rightW := 3 + 1 + volCells + 1 + DispW(volVal) + 2 + mtW

	return between(left, leftW, right, rightW, W)
}

// dividerRow is a section separator: the label centred between two dim rules,
// "──── label ────", W cells wide, so the title reads as a heading.
func (m *model) dividerRow(label string, W int) string {
	rule := W - DispW(label) - 2 // a space flanks the label on each side
	if rule < 0 {
		rule = 0
	}
	left := rule / 2
	bar := func(n int) string { return m.sty.sDmr.Render(strings.Repeat(GL["track"], n)) }
	return bar(left) + " " + m.sty.sDim.Render(label) + " " + bar(rule-left)
}

// eqStrip lays the 7 bands out in three groups divided by thin vertical rules: the
// EQ switch + tone (EQ Treble Mid Bass) | the deep bass (Sub Lvl) | the output cap
// (Max Vol), which sits last as it is rarely touched.
func (m *model) eqStrip(s protocol.Snapshot, W, barH int) []string {
	_, vals := m.st.EQView()
	n := len(eqOrder)
	const sepW = 3
	sepAfter := map[int]bool{3: true, 5: true} // groups: EQ+tone | Sub Lvl | Max Vol
	widths := splitWidth(W-len(sepAfter)*sepW, n)
	sep := m.eqSeparator(barH, sepW)
	blocks := make([]string, 0, n+len(sepAfter))
	for d := 0; d < n; d++ {
		band := m.eqBand(eqOrder[d], vals, m.pane == paneEQ && m.eqFocus == d, widths[d], barH)
		blocks = append(blocks, strings.Join(band, "\n"))
		if sepAfter[d] {
			blocks = append(blocks, sep)
		}
	}
	return strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, blocks...), "\n")
}

// eqSeparator is a full-height dim vertical rule (centred in w cells) between the
// EQ groups; its height matches a band: label + blank + barH bars + value + underline.
func (m *model) eqSeparator(barH, w int) string {
	line := ccell(m.sty.sDmr.Render("┃"), w)
	rows := make([]string, barH+4)
	for i := range rows {
		rows[i] = line
	}
	return strings.Join(rows, "\n")
}

// eqBand builds one vertical band column: label, bar/toggle, value, underline.
func (m *model) eqBand(specIdx int, vals map[string]int, focused bool, colW, barH int) []string {
	sp := tunnel.Specs[specIdx]
	v, known := vals[sp.Code]

	labelPen := m.sty.sDim
	if focused {
		labelPen = m.sty.sAcc.Bold(true)
	}
	// label, a breathing line, then the bar — matching the volume rail's
	// "Vol" / gap / bar spacing. The label is clipped so a long name like
	// "Deep Bass" never wraps a narrow column.
	out := []string{ccell(labelPen.Render(Clip(eqShort[sp.Code], colW)), colW), ccell("", colW)}

	if sp.Kind == tunnel.Toggle {
		// a 2-position toggle switch on a dim track: a bright "●" knob at the TOP
		// for on, a hollow "○" knob at the BOTTOM for off — position and fill both
		// signal the state, far clearer than a lone middle dot.
		knobRow, knob, knobPen, val, valPen := -1, "", m.sty.sDim, "—", m.sty.sDim
		if known {
			if v != 0 {
				knobRow, knob, knobPen, val, valPen = 0, "●", m.sty.sAcc, "on", m.sty.sAcc
			} else {
				knobRow, knob, val = barH-1, "○", "off"
			}
		}
		for r := 0; r < barH; r++ {
			if r == knobRow {
				out = append(out, ccell(knobPen.Render(knob), colW))
			} else {
				out = append(out, ccell(m.sty.sDmr.Render("┃"), colW))
			}
		}
		out = append(out, ccell(valPen.Render(val), colW))
	} else {
		frac := 0.0
		if known {
			frac = float64(v-sp.Min) / float64(sp.Max-sp.Min)
		}
		for _, bl := range m.sty.vbar(frac, barH) {
			out = append(out, ccell(bl, colW))
		}
		val := "—"
		if known {
			if sp.Min < 0 {
				val = toneStr(v)
			} else {
				val = strconv.Itoa(v)
			}
		}
		valPen := m.sty.sDim
		if focused {
			valPen = m.sty.sBri
		}
		out = append(out, ccell(valPen.Render(val), colW))
	}

	under := ccell("", colW)
	if focused {
		uw := DispW(eqShort[sp.Code])
		if uw > colW { // never wider than the column, or the band grows a row
			uw = colW
		}
		under = ccell(m.sty.sAcc.Render(strings.Repeat(GL["track"], uw)), colW)
	}
	return append(out, under)
}

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
			return fmt.Sprintf("%s%+d", eqShort[code][:1], v)
		}
		return fmt.Sprintf("%s %d", eqShort[code], v)
	}
	line := strings.Join([]string{part("MXV"), part("EQS"), part("TRE"), part("MID"), part("BAS"), part("VBS"), part("VBI")}, " · ")
	return m.sty.sDim.Render(Clip(line, W))
}

func (m *model) footerRow(W int) string {
	var hint string
	if m.pane == paneEQ {
		hint = "←→ pick · ↑↓ adjust · enter toggle · tab player · q quit"
	} else {
		hint = "space play · ↑↓ vol · m mute · e/tab EQ · ? diag · q quit"
	}
	return lipgloss.NewStyle().Width(W).Align(lipgloss.Right).
		Render(m.sty.sDmr.Render(Clip(hint, W)))
}

func (m *model) renderDiag(s protocol.Snapshot, now time.Time, W int) string {
	t := m.sty
	lastRx, dData, att, derr, si := m.st.DiagView()
	dev := m.st.DevInfoView()
	eqConn, eqv := m.st.EQView()

	gw := min(20, W-52) // gauge width, leaving room for label/value/detail
	if gw < 8 {
		gw = 8
	}
	// lower-is-better health picker (good < a <= warn < b <= bad)
	lo := func(v, a, b float64) lipgloss.Style {
		switch {
		case v < a:
			return t.sAcc
		case v < b:
			return stWarn
		default:
			return stRed
		}
	}

	var L []string
	add := func(s string) { L = append(L, s) }

	clock := now.Format("15:04")
	var hr string
	var hrW int
	switch {
	case !s.Connected:
		hr, hrW = stWarn.Render("● disconnected"), DispW("● disconnected")
	case !dData.IsZero() && now.Sub(dData).Seconds() > 3:
		hr, hrW = stWarn.Render("● LUCI silent · "+clock), DispW("● LUCI silent · "+clock)
	default:
		hr, hrW = t.sAcc.Render("●")+t.sDim.Render(" connected · "+clock), DispW("● connected · "+clock)
	}
	add(between(t.sAcc.Bold(true).Render("diagnostics"), DispW("diagnostics"), hr, hrW, W))
	add("")

	host := m.cfg.User + "@" + m.cfg.Host
	model, os, fw, cores, up, build, mac := "—", "—", "—", "—", "—", "—", "—"
	if si != nil {
		if si.FW != "" {
			fw, model = si.FW, "Arylic "+firstSeg(si.FW, '_')
		}
		if si.OS != "" {
			os = strings.Replace(si.OS, "-", " ", 1)
		}
		if si.NCPU != "" {
			cores = si.NCPU
		}
		up = fmtUptime(si.Up)
	}
	if dev != nil {
		if dev.IP != "" {
			host = m.cfg.User + "@" + dev.IP
		}
		if dev.Platform != "" && model != "—" {
			model += " · " + dev.Platform
		}
		if dev.Build != "" {
			build = dev.Build
			if dev.App != "" {
				build += " · app " + dev.App
			}
		}
		if dev.MAC != "" {
			mac = dev.MAC
		}
	}
	add(m.gridRow("host", host, "uptime", up, W))
	add(m.gridRow("device", model, "os", os, W))
	add(m.gridRow("firmware", fw, "build", build, W))
	add(m.gridRow("mac", mac, "cores", cores, W))

	add(m.dividerRow("connection", W))
	rxTxt, rxPen := "—", t.sDim
	if !lastRx.IsZero() {
		secs := now.Sub(lastRx).Seconds()
		rxTxt, rxPen = fmt.Sprintf("%.1fs", secs), lo(secs, 3, 8)
	}
	attWord := "attempts"
	if att == 1 {
		attWord = "attempt"
	}
	add(m.diagLine("player", t.sTxt.Render("ssh stream · rx ")+rxPen.Render(rxTxt)+
		t.sTxt.Render(fmt.Sprintf(" ago · %d %s", att, attWord))))
	tunTxt, tunPen := "down", stRed
	if eqConn {
		tunTxt, tunPen = "live", t.sAcc
	}
	add(m.diagLine("control", t.sTxt.Render("tunnel :2018 · ")+tunPen.Render(tunTxt)))

	if dev != nil && (dev.SSID != "" || dev.IP != "") {
		add(m.dividerRow("network", W))
		band := ""
		if f, err := strconv.Atoi(dev.Freq); err == nil && f > 0 {
			b := " · 2.4 GHz"
			if f >= 5000 {
				b = " · 5 GHz"
			}
			band = fmt.Sprintf(" · ch %d%s", freqToChan(f), b)
		}
		add(m.diagLine("wi-fi", t.sBri.Render(orDash(dev.SSID))+t.sDim.Render(band)))
		if si != nil {
			if dbm, err := strconv.Atoi(si.SignalDBm); err == nil {
				pen := lo(float64(-dbm), 60, 72) // -dBm: 41 good, 72 warn
				valTxt := fmt.Sprintf("%d dBm", dbm)
				detail := ""
				if dev.Rate != "" {
					detail = "   " + dev.Rate + " Mbit/s"
				}
				if lq, e := strconv.Atoi(si.LinkQ); e == nil && lq > 0 {
					detail += fmt.Sprintf("  · link %d/70", lq)
				}
				detail = Clip(detail, W-10-gw-2-DispW(valTxt)) // never wrap the row
				add(m.diagGauge("signal", t.gaugeBar(float64(dbm+90)/60, gw, pen),
					pen.Render(valTxt), detail))
			}
			if si.TxRetryDeltaOK {
				add(m.diagLine("retries", t.sTxt.Render(strconv.Itoa(si.TxRetryDelta))+t.sDmr.Render(" tx · since connect")))
			}
		}
		add(m.diagLine("address", t.sTxt.Render(orDash(dev.IP))+t.sDim.Render(" · gw "+orDash(dev.Gateway))))
	}

	add(m.dividerRow("audio", W))
	formatTxt := "—"
	if tr := s.Track; tr != nil {
		var ps []string
		if q := Quality(tr); q != "" {
			ps = append(ps, q)
		}
		if ch := tr.GetInt("ChannelCount"); ch > 0 {
			ps = append(ps, fmt.Sprintf("%d ch", ch))
		}
		if len(ps) > 0 {
			formatTxt = strings.Join(ps, " · ")
		}
	}
	add(m.diagLine("format", t.sTxt.Render(formatTxt)))
	volPen, volTxt := t.sAcc, fmt.Sprintf("%d%%", s.Vol)
	if s.Muted {
		volPen, volTxt = stRed, "muted"
	}
	add(m.diagGauge("volume", t.gaugeBar(float64(s.Vol)/100, gw, volPen), volPen.Render(volTxt), ""))
	add(m.diagLine("eq", m.eqReadout(eqv)))

	add(m.dividerRow("resources", W))
	if si != nil {
		loads := strings.Fields(si.Load)
		nc, _ := strconv.Atoi(si.NCPU)
		if nc < 1 {
			nc = 1
		}
		if len(loads) >= 1 {
			if l1, err := strconv.ParseFloat(loads[0], 64); err == nil {
				frac := l1 / float64(nc)
				pen := lo(frac*100, 60, 85)
				detail := "   1m " + loads[0]
				if len(loads) >= 3 {
					detail += " · 5m " + loads[1] + " · 15m " + loads[2]
				}
				add(m.diagGauge("cpu", t.gaugeBar(frac, gw, pen),
					pen.Render(fmt.Sprintf("%d%%", int(frac*100+0.5))), detail))
			}
		}
		av, e1 := strconv.Atoi(si.Avail)
		tot, e2 := strconv.Atoi(si.Total)
		if e1 == nil && e2 == nil && tot > 0 {
			uf := float64(tot-av) / float64(tot)
			pen := lo(uf*100, 70, 88)
			add(m.diagGauge("memory", t.gaugeBar(uf, gw, pen),
				pen.Render(fmt.Sprintf("%d%%", int(uf*100+0.5))),
				fmt.Sprintf("   %d / %d MB free", av/1024, tot/1024)))
		}
		if mc, err := strconv.Atoi(si.TempmC); err == nil {
			c := mc / 1000
			pen := lo(float64(c), 60, 75)
			add(m.diagGauge("temp", t.gaugeBar(float64(c)/85, gw, pen),
				pen.Render(fmt.Sprintf("%d °C", c)), "   SoC"))
		}
	}
	if dev != nil {
		used, e1 := strconv.Atoi(dev.DataUsed)
		tot, e2 := strconv.Atoi(dev.DataTotal)
		if e1 == nil && e2 == nil && tot > 0 {
			uf := float64(used) / float64(tot)
			pen := lo(uf*100, 80, 92)
			add(m.diagGauge("storage", t.gaugeBar(uf, gw, pen),
				pen.Render(fmt.Sprintf("%d%%", int(uf*100+0.5))),
				fmt.Sprintf("   %d / %d MB used · data", used/1024, tot/1024)))
		}
	}

	if derr != "" {
		add("")
		add(stWarn.Render(Clip(GL["warn"]+" "+derr, W)))
	}
	add("")
	add(t.sDmr.Render("live · any key returns to the dashboard"))

	// never overflow the frame: cap to its inner height
	if budget := m.rows - 4; budget > 2 && len(L) > budget {
		L = L[:budget]
		L[budget-1] = t.sDmr.Render("… resize for more")
	}
	return strings.Join(L, "\n")
}

// gridRow renders a two-column "label value | label value" row, exactly W wide.
func (m *model) gridRow(k1, v1, k2, v2 string, W int) string {
	half := W / 2
	return m.cellKV(k1, v1, half) + m.cellKV(k2, v2, W-half)
}

func (m *model) cellKV(k, v string, w int) string {
	const labW = 9
	vv := Clip(v, w-labW)
	out := m.sty.sDim.Render(k) + strings.Repeat(" ", labW-DispW(k)) + m.sty.sTxt.Render(vv)
	if vis := labW + DispW(vv); vis < w {
		out += strings.Repeat(" ", w-vis)
	}
	return out
}

// diagLine renders "label  value" with a fixed dim label column.
func (m *model) diagLine(label, value string) string {
	return m.sty.sDim.Render(label) + strings.Repeat(" ", 10-DispW(label)) + value
}

// diagGauge renders "label  [gauge]  value detail".
func (m *model) diagGauge(label, gauge, value, detail string) string {
	return m.sty.sDim.Render(label) + strings.Repeat(" ", 10-DispW(label)) +
		gauge + "  " + value + m.sty.sDmr.Render(detail)
}

// eqReadout renders the compact EQ/tone summary line from the tunnel values.
func (m *model) eqReadout(vals map[string]int) string {
	t := m.sty
	var parts []string
	if v, ok := vals["EQS"]; ok {
		st, pen := "off", t.sDim
		if v != 0 {
			st, pen = "on", t.sAcc
		}
		parts = append(parts, pen.Render("EQ "+st))
	}
	for _, c := range []struct{ code, lbl string }{{"TRE", "T"}, {"MID", "M"}, {"BAS", "B"}} {
		if v, ok := vals[c.code]; ok {
			parts = append(parts, t.sDim.Render(c.lbl+" ")+t.sTxt.Render(toneStr(v)))
		}
	}
	if v, ok := vals["VBS"]; ok {
		st, pen := "off", t.sDim
		if v != 0 {
			st, pen = "on", t.sAcc
		}
		d := "Sub " + st
		if vi, ok := vals["VBI"]; ok && v != 0 {
			d += " " + strconv.Itoa(vi)
		}
		parts = append(parts, pen.Render(d))
	}
	if v, ok := vals["MXV"]; ok { // Max Vol last — the rarely-changed output cap
		parts = append(parts, t.sBri.Render("Max Vol "+strconv.Itoa(v)))
	}
	if len(parts) == 0 {
		return t.sDim.Render("—")
	}
	return strings.Join(parts, t.sDmr.Render(" · "))
}

func firstSeg(s string, sep byte) string {
	if i := strings.IndexByte(s, sep); i >= 0 {
		return s[:i]
	}
	return s
}

func freqToChan(mhz int) int {
	switch {
	case mhz == 2484:
		return 14
	case mhz >= 2412 && mhz <= 2472:
		return (mhz-2412)/5 + 1
	case mhz >= 5000:
		return (mhz - 5000) / 5
	}
	return 0
}

func fmtUptime(up string) string {
	secs, err := strconv.ParseFloat(strings.TrimSpace(up), 64)
	if err != nil || secs < 0 {
		return "—"
	}
	s := int(secs)
	switch d, h, mn := s/86400, s%86400/3600, s%3600/60; {
	case d > 0:
		return fmt.Sprintf("%dd %dh %dm", d, h, mn)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, mn)
	default:
		return fmt.Sprintf("%dm", mn)
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// toneStr formats a signed tone value: "+3", "0", "-6" (avoids an odd "+0").
func toneStr(v int) string {
	if v == 0 {
		return "0"
	}
	return fmt.Sprintf("%+d", v)
}

// ---- small layout helpers ----------------------------------------------------

// between places left- and right-aligned styled segments W columns apart, using
// the segments' known visible widths (styled strings carry ANSI codes).
func between(left string, leftW int, right string, rightW int, W int) string {
	gap := W - leftW - rightW
	if gap < 1 {
		gap = 1
	}
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

// preloadSnapshot seeds State from a cached snapshot for an instant first paint.
func preloadSnapshot(st *protocol.State, cached map[string]interface{}) {
	if cached == nil {
		return
	}
	track := protocol.SanitizeTrack(cached["track"])
	if len(track) == 0 {
		track = nil
	}
	pos, _ := protocol.Int(cached["pos"])
	vol, _ := protocol.Int(cached["vol"])
	st.Preload(track, pos, vol)

	// Seed last-known EQ/tone values so the equalizer paints instantly, before
	// the :2018 tunnel finishes its seed queries. Unknown codes and non-numeric
	// values are dropped; everything is clamped to its control's range.
	if eq, ok := cached["eq"].(map[string]interface{}); ok {
		vals := make(map[string]int, len(eq))
		for code, raw := range eq {
			if _, known := tunnel.Lookup(code); !known {
				continue
			}
			if n, ok := protocol.Int(raw); ok {
				vals[code] = tunnel.Clamp(code, n)
			}
		}
		if len(vals) > 0 {
			st.PreloadEQ(vals)
		}
	}
}

// Run wires up State, the worker goroutines, and the Bubble Tea program, then
// tears everything down on exit. Returns the process exit code: 0 clean quit,
// 130 Ctrl-C, 143 SIGTERM/SIGHUP.
func Run(cfg config.Config) (int, error) {
	st := protocol.NewState()
	st.PremuteFile = config.PremutePath(cfg)
	st.SnapshotFile = config.SnapshotPath(cfg)
	if cfg.Warn != "" {
		st.Note(cfg.Warn)
	}
	preloadSnapshot(st, config.LoadSnapshot(st.SnapshotFile))

	cmds := make(chan *protocol.Command, 1024)
	eqcmds := make(chan workers.EQCommand, 64)
	go workers.StreamWorker(st, cfg)
	go workers.CommandWorker(st, cmds, workers.CommandDeadline)
	go workers.Watchdog(st, workers.SilentAfter, workers.ConnectWindow, workers.DatalessAfter)
	go workers.TunnelWorker(st, cfg, eqcmds)

	m := newModel(st, cfg, cmds, eqcmds)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithoutSignalHandler())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	defer signal.Stop(sigCh)
	sigCode := &atomic.Int32{}
	sigCode.Store(-1)
	stopSig := make(chan struct{})
	go func() {
		select {
		case sig := <-sigCh:
			if sig == syscall.SIGINT {
				sigCode.Store(130)
			} else {
				sigCode.Store(143)
			}
			p.Quit()
		case <-stopSig:
		}
	}()

	finalModel, runErr := p.Run()
	close(stopSig)

	workers.Teardown(st, cmds, workers.DrainTimeout)
	fmt.Fprint(os.Stdout, "\x1b]0;\x07") // reset the terminal title

	switch {
	case sigCode.Load() >= 0:
		return int(sigCode.Load()), nil
	case runErr != nil:
		return 1, runErr
	default:
		if fm, ok := finalModel.(*model); ok && fm.interrupted {
			return 130, nil
		}
		return 0, nil
	}
}
