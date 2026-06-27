package tui

import (
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"
	"unsafe"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lucasdaddiego/lp10/internal/artwork"
	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/tunnel"
	"github.com/lucasdaddiego/lp10/internal/workers"
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

// sliderLabelW / sliderValW are the fixed-width columns shared by eqSliders (the
// renderer) and recordFullZones (the mouse hit-zone builder). Both must agree so a
// click on the rendered slider maps to the correct track position.
const (
	sliderLabelW = 8 // "Max Vol " — label column: longest name "Max Vol" (7) + 1 space
	sliderValW   = 4 // right-aligned value column: " +10", " 100", "  -1", …
)

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

	focus         int  // transport-button focus (index into actions)
	pane          int  // paneNow | paneEQ
	eqFocus       int  // EQ-strip display position (index into eqOrder)
	frame         int  // animation frame for the art motif (advances while playing)
	motifLive     bool // the plasma motif was actually drawn last render (gates the fast frame tick)
	sonarLive     bool // the connecting sonar was drawn last render (keeps the frame clock ticking while idle)
	scroll        int  // tick counter driving the now-playing marquee (advances every tick)
	diag          bool
	showRemaining bool
	flash         map[string]time.Time

	rows, cols   int
	cellW, cellH int // terminal cell size in device px (0 if unknown); sizes the Kitty cover
	curTitle     string

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

	// album-art cache: rasterizing the cover to half-blocks is keyed by
	// (url,w,h), so a steady cover reuses the last raster rather than
	// re-rasterizing every frame. Cleared implicitly when the key changes.
	artBlk []string
	artKey artKey

	// ghost-cover cache: the dimmed last cover shown in the idle slot, keyed like
	// artBlk but in its own field so it never collides with the live cover.
	ghostBlk []string
	ghostKey artKey

	// ambient tint: the seek bar / cover frame / status dot recoloured to the
	// current cover's dominant hue. amb is nil for the theme default (no cover,
	// greyscale art, or art disabled); ambKey is the CoverURL it was computed for
	// (recompute only on a cover change, including a deliberate nil result).
	amb    *ambientTint
	ambKey string

	// mouse hit-zones recorded by the last dashboard render, in absolute terminal
	// coordinates. Update consults them on a MouseMsg; they describe the frame the
	// user is actually looking at and clicking on. Reset every View, populated only
	// by renderDashboard (nil in the mini / diagnostics views).
	mzBtns []btnZone // transport + mute buttons (press to fire)
	mzVol  volZone   // the volume rail / meter (press or drag to set by position)
	mzEQ   []eqZone  // the EQ band columns (full layout only)

	interrupted bool // Ctrl-C, so Run can exit 130 like Python's KeyboardInterrupt

	sty *theme
}

// motif returns the cached art block, recomputing only when (w,h,frame) changes.
func (m *model) motif(w, h int) []string {
	m.motifLive = true // the animated plasma is actually on screen this frame
	key := [3]int{w, h, m.frame}
	if m.motifBlk == nil || m.motifKey != key {
		m.motifBlk = m.sty.motifBlock(w, h, m.frame)
		m.motifKey = key
	}
	return m.motifBlk
}

// artRender is the chosen album-art renderer for the current frame.
type artRender int

const (
	artMotif artRender = iota // procedural plasma (no art, or unsupported terminal)
	artHalf                   // 24-bit half-block raster (any truecolor terminal)
	artKitty                  // Kitty true-pixel image (Ghostty / kitty)
)

// kittyImageID is the fixed Kitty image id every cover is transmitted under. One
// id suffices: the current cover is (re)loaded into it whenever the art changes,
// immediately before the placeholders that reference it are painted, so a stale
// image is never shown. The value is arbitrary but stable. The idle ghost cover
// rides a separate id so the two never share a placement slot.
const (
	kittyImageID = 1981
	kittyGhostID = 1982
)

// artKey identifies a rasterized cover by source URL, cell dimensions, the cell
// pixel size (the Kitty PNG is sized to it), and the renderer that produced it
// (so switching modes, sizes, or fonts rebuilds the cache).
type artKey struct {
	url    string
	w, h   int
	cw, ch int
	mode   artRender
}

// artChoice resolves which renderer to use for a loaded cover, honoring the
// art_mode config and the terminal's capabilities, degrading kitty → half-block
// → motif as support runs out.
func (m *model) artChoice() artRender {
	if !m.cfg.Art {
		return artMotif
	}
	switch m.cfg.ArtMode {
	case "off":
		return artMotif
	case "halfblock":
		if m.sty.trueColor {
			return artHalf
		}
	case "kitty":
		// explicit override: force the Kitty path even when auto-detection didn't
		// fire (e.g. a terminal that supports the protocol but isn't fingerprinted).
		// artColumn still degrades to half-block / motif if the encode yields nothing.
		return artKitty
	default: // "auto"
		if m.sty.kittyGraphics {
			return artKitty
		}
		if m.sty.trueColor {
			return artHalf
		}
	}
	return artMotif
}

// artColumn renders the left art panel: the real album cover (Kitty pixels or a
// half-block raster) when one is loaded and supported, otherwise the procedural
// plasma motif (radio/idle, art disabled, or a lesser terminal). The raster is
// cached by (url,w,h,mode) so a steady cover costs nothing per frame; the Kitty
// transmit rides the first line, so it re-sends only when that line repaints.
func (m *model) artColumn(s protocol.Snapshot, w, h int) []string {
	if s.Track == nil {
		return m.idleArt(s, w, h) // nothing playing: sonar while connecting, else the ghost cover
	}
	if s.Art == nil {
		return m.motif(w, h) // playing without a cover (radio): the plasma motif
	}
	mode := m.artChoice()
	if mode == artMotif {
		return m.motif(w, h)
	}
	key := artKey{s.CoverURL, w, h, m.cellW, m.cellH, mode}
	if m.artBlk == nil || m.artKey != key {
		var built []string
		switch mode {
		case artKitty:
			if transmit, lines := artwork.KittyImage(s.Art, w, h, kittyImageID, w*m.cellW, h*m.cellH); len(lines) > 0 {
				lines[0] = transmit + lines[0] // zero-width: loads the image, then the cells composite it
				built = lines
			} else if m.sty.trueColor {
				built = artwork.HalfBlock(s.Art, w, h) // encode failed: degrade in place
			}
		case artHalf:
			built = artwork.HalfBlock(s.Art, w, h)
		}
		if built == nil {
			return m.motif(w, h) // give up to the motif without poisoning the cache
		}
		m.artBlk, m.artKey = built, key
	}
	return m.artBlk
}

// noteMotif is the small beamed-pair glyph drawn in the idle cover slot — two
// stems under a beam over two note heads, so an empty box reads as "music,
// paused" rather than abandoned. Plain box/▪ glyphs (all width-1 to charW).
var noteMotif = []string{"┏━━━┓", "┃   ┃", "●   ●"}

// refreshAmbient recomputes the per-album tint when the cover changes. It clears
// the tint whenever no real cover is on screen (idle, radio, art disabled, or a
// lesser terminal), and otherwise derives it from the cover's dominant hue —
// keeping the theme default for a greyscale cover (DominantOK==false).
func (m *model) refreshAmbient(s protocol.Snapshot) {
	if s.Art == nil || m.artChoice() == artMotif {
		m.amb, m.ambKey = nil, ""
		return
	}
	if m.ambKey == s.CoverURL {
		return // already resolved (tint or deliberate nil) for this cover
	}
	m.ambKey = s.CoverURL
	if s.DominantOK {
		m.amb = m.sty.tint(s.Dominant) // hue precomputed off the render path by the art worker
	} else {
		m.amb = nil
	}
}

// idleArt fills the cover slot when nothing is playing, telling the connection
// state at a glance: a live radar sweep while (re)connecting, then — once
// connected and simply idle — a dimmed "ghost" of the last cover played (a calm
// note motif when there's no cover to recall). boxArt adds the frame around it.
func (m *model) idleArt(s protocol.Snapshot, w, h int) []string {
	if !s.Connected {
		m.sonarLive = true // keep the frame clock ticking so the beam keeps sweeping
		return m.sty.sonar(w, h, m.frame)
	}
	if g := m.ghostCover(s, w, h); g != nil {
		return g
	}
	return m.noteBox(w, h)
}

// ghostCover renders the last-played cover dimmed and desaturated — a faint
// memory in the idle slot — reusing the Kitty / half-block path on a ghosted
// image, cached in its own slot. Returns nil when there's nothing to recall or
// art is off, so the caller can fall back to the note motif.
func (m *model) ghostCover(s protocol.Snapshot, w, h int) []string {
	mode := m.artChoice()
	if s.LastArt == nil || mode == artMotif {
		return nil
	}
	key := artKey{url: "ghost:" + s.LastCoverURL, w: w, h: h, cw: m.cellW, ch: m.cellH, mode: mode}
	if m.ghostBlk == nil || m.ghostKey != key {
		img := artwork.Ghost(s.LastArt)
		var built []string
		switch mode {
		case artKitty:
			if transmit, lines := artwork.KittyImage(img, w, h, kittyGhostID, w*m.cellW, h*m.cellH); len(lines) > 0 {
				lines[0] = transmit + lines[0]
				built = lines
			} else if m.sty.trueColor {
				built = artwork.HalfBlock(img, w, h)
			}
		case artHalf:
			built = artwork.HalfBlock(img, w, h)
		}
		if built == nil {
			return nil
		}
		m.ghostBlk, m.ghostKey = built, key
	}
	return m.ghostBlk
}

// noteBox draws the calm note motif centred in a w×h field — the idle fallback
// when there's no cover to ghost — falling back to a single ♪ in a box too small
// for the motif (or under a CJK locale).
func (m *model) noteBox(w, h int) []string {
	out := make([]string, h)
	blank := strings.Repeat(" ", w)
	for i := range out {
		out[i] = blank
	}
	const nw = 5 // width of every noteMotif line
	if amb == 2 || w < nw || h < len(noteMotif) {
		if g := GL["note"]; h > 0 && w >= DispW(g) {
			col := (w - DispW(g)) / 2
			out[h/2] = strings.Repeat(" ", col) + m.sty.sDim.Render(g) + strings.Repeat(" ", w-col-DispW(g))
		}
		return out
	}
	top := (h - len(noteMotif)) / 2
	col := (w - nw) / 2
	for i, ln := range noteMotif {
		out[top+i] = strings.Repeat(" ", col) + m.sty.sDmr.Render(ln) + strings.Repeat(" ", w-col-nw)
	}
	return out
}

// boxArt wraps art (each line contentW display columns wide) in a thin
// box-drawing frame, so the cover reads as a framed print rather than a floating
// image. The frame is bevelled — the top and left edges lit, the bottom and
// right edges in shadow — so the cover lifts off the background as if lit from
// the top-left. The lit edge takes the album's ambient hue when one is active.
// The result is contentW+2 wide and len(art)+2 tall.
func (m *model) boxArt(art []string, contentW int) []string {
	lit := m.sty.sDim
	if m.amb != nil {
		lit = m.amb.frame
	}
	shadow := m.sty.sDmr
	h := strings.Repeat(GL["h"], contentW)
	leftBar, rightBar := lit.Render(GL["v"]), shadow.Render(GL["v"])
	out := make([]string, 0, len(art)+2)
	out = append(out, lit.Render(GL["tl"]+h+GL["tr"]))
	for _, line := range art {
		out = append(out, leftBar+line+rightBar)
	}
	return append(out, shadow.Render(GL["bl"]+h+GL["br"]))
}

func newModel(st *protocol.State, cfg config.Config, cmds chan *protocol.Command, eqcmds chan workers.EQCommand) *model {
	m := &model{
		st: st, cfg: cfg, cmds: cmds, eqcmds: eqcmds,
		focus:         1,
		showRemaining: true,
		flash:         map[string]time.Time{},
	}
	m.cellW, m.cellH = cellPixelSize() // refreshed on every resize; sizes the Kitty cover
	return m
}

// cellPixelSize reports the terminal's cell size in device pixels (width, height)
// via TIOCGWINSZ, or (0, 0) when the terminal doesn't report pixel dimensions (or
// stdout isn't a tty, e.g. in tests). The Kitty cover path sizes its image to the
// cover's exact pixel footprint, since a virtual placement is drawn at the image's
// native resolution rather than scaled to the cell box.
func cellPixelSize() (w, h int) {
	var ws struct{ rows, cols, xpix, ypix uint16 }
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdout.Fd(),
		uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws))); errno != 0 {
		return 0, 0
	}
	if ws.cols == 0 || ws.rows == 0 || ws.xpix == 0 || ws.ypix == 0 {
		return 0, 0
	}
	return int(ws.xpix) / int(ws.cols), int(ws.ypix) / int(ws.rows)
}

// Two cadences drive the UI. The logic tick (100ms) advances the marquee, the
// stats keep-alive, and the window title — its constants (marqueeColTicks,
// StatsReassertTicks) are counted in these 100ms units. The frame tick is the
// animation clock, decoupled so the plasma motif and the extrapolated seek bar
// glide at ~30fps on a GPU terminal without speeding up the logic above. It
// idles to a gentle rate while paused/idle, when the motif is frozen and the
// motif cache makes those wake-ups nearly free.
type logicMsg struct{}
type frameMsg struct{}

const (
	logicInterval = 100 * time.Millisecond
	framePlaying  = 33 * time.Millisecond  // ~30fps while playing
	frameSonar    = 70 * time.Millisecond  // ~14fps: calm expanding rings while connecting
	frameIdle     = 250 * time.Millisecond // frozen motif: just keep the clock alive
)

func logicTick() tea.Cmd {
	return tea.Tick(logicInterval, func(time.Time) tea.Msg { return logicMsg{} })
}

func frameTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return frameMsg{} })
}

func (m *model) Init() tea.Cmd { return tea.Batch(logicTick(), frameTick(framePlaying)) }

// nbSend enqueues v without ever blocking the caller: on a full buffer it drops
// the oldest queued item and retries once. Stale commands are coalesced/aged-out
// downstream, so a dropped one is harmless. Shared by the transport (send) and
// EQ (sendEQ) paths.
func nbSend[T any](ch chan T, v T) {
	select {
	case ch <- v:
	default:
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- v:
		default:
		}
	}
}

// send enqueues a transport command without ever blocking the update loop.
func (m *model) send(mid int, data string) {
	nbSend(m.cmds, &protocol.Command{Mid: mid, Data: data, TS: time.Now()})
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
	nbSend(m.eqcmds, workers.EQCommand{Code: code, Val: val})
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
			m.eqFocus = (m.eqFocus - 1 + len(eqOrder)) % len(eqOrder) // select band above
		} else {
			m.do("volup")
		}
		return ""
	case kDown:
		if m.pane == paneEQ {
			m.eqFocus = (m.eqFocus + 1) % len(eqOrder) // select band below
		} else {
			m.do("voldn")
		}
		return ""
	case kLeft:
		if m.pane == paneEQ {
			m.eqAdjust(-1) // nudge the focused slider left (decrease value)
		} else {
			m.focus = (m.focus - 1 + len(actions)) % len(actions)
		}
		return ""
	case kRight:
		if m.pane == paneEQ {
			m.eqAdjust(+1) // nudge the focused slider right (increase value)
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
		m.cellW, m.cellH = cellPixelSize() // window px changed; refresh the cover's pixel footprint
		return m, nil
	case logicMsg:
		cmds := []tea.Cmd{logicTick()}
		m.scroll++       // advance the now-playing marquee (independent of play state)
		s := m.st.Snap() // one snapshot per tick, reused below
		m.syncStats()    // device emits @@s only while the diag overlay is open
		if title := m.computeTitle(s); title != m.curTitle {
			m.curTitle = title
			cmds = append(cmds, tea.SetWindowTitle(title))
		}
		return m, tea.Batch(cmds...)
	case frameMsg:
		// Advance the animation clock only when something on screen is animating:
		// the plasma motif while playing (frozen when paused), or the connecting
		// sonar in the idle cover slot. Otherwise (album art, ghost cover, paused)
		// nothing moves, so idle the clock — the 100ms logic tick still drives slow
		// updates. m.motifLive / m.sonarLive are set by the last render.
		fs := m.st.Snap()
		next := frameIdle
		switch {
		case m.motifLive && fs.Playing == 0:
			m.frame++
			next = framePlaying
		case m.sonarLive && !fs.Connected:
			m.frame++
			next = frameSonar
		}
		return m, frameTick(next)
	case tea.MouseMsg:
		m.handleMouse(msg)
		return m, nil
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
		Padding(1, 2, 0, 2). // breathing room between the border and the content
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
	inner := m.rows - 3

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
		coverH := (inner - 2 - len(tail)) - 2 // region minus the 2 frame rows
		if coverH > coverHCap {               // a tasteful record sleeve, not a billboard
			coverH = coverHCap
		}
		if coverH < 6 {
			coverH = 6
		}
		cellAR := 2.0 // cell height ÷ width; the column count that squares the box
		if m.cellW > 0 && m.cellH > 0 {
			cellAR = float64(m.cellH) / float64(m.cellW)
		}
		coverW := int(float64(coverH)*cellAR + 0.5)
		if maxW := W - 37; coverW > maxW { // reserve room for the metadata + volume columns
			coverW = maxW
			coverH = int(float64(coverW)/cellAR + 0.5)
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

// recordFullZones records the transport, volume-rail, and EQ-band hit-zones for
// the full dashboard, in absolute terminal coordinates. It mirrors the geometry
// of the full branch above: the block is vertically centred by stack between a
// two-line top ([header, ""]) and the bottom tail, and the bottom tail (EQ sliders
// + footer) is pinned to the inner region's foot.
func (m *model) recordFullZones(coverW, midW, blockH, midLen, tailLen, inner, W int) {
	region := inner - 2 - tailLen // stack's middle region (below [header,""], above tail)
	if region < 0 {
		region = 0
	}
	middleLen := blockH // stack clips the block to the region if it overflows
	if middleLen > region {
		middleLen = region
	}
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
	trackW := W - sliderLabelW - sliderValW
	if trackW < 1 {
		trackW = 1
	}
	for d := 0; d < len(eqOrder); d++ {
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
	region := h - len(top) - len(bottom)
	if region < 0 {
		region = 0
	}
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
	nameMax := W - prefixW - 2 - statW - volW - 4
	if nameMax > 24 {
		nameMax = 24
	}
	if nameMax < 4 {
		nameMax = 4
	}
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
	// Make the title and artist clickable (OSC 8) where the terminal supports
	// it — a degrades-to-plain enhancement, so it's always on. The link wraps
	// the fully styled+marqueed line so no later width math (DispW) ever sees
	// the URL bytes; downstream layout measures via lipgloss, which ignores it.
	// The source/format ("Spotify · Ogg · 44.1 kHz") rides the header row, not
	// here, so the now-playing block stays a tight two lines.
	artist := t.Str("Artist")
	trackLink := spotifySearch(strings.TrimSpace(name + " " + artist))
	secondLink := spotifySearch(artist)
	if secondLink == "" {
		secondLink = spotifySearch(t.Str("Album"))
	}
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
	cluster := w
	if cluster > maxCluster {
		cluster = maxCluster
	}
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
	name := t.Str("TrackName")
	if name == "" {
		name = "—"
	}
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
	if s.Muted {
		// Impossible to miss: a SOLID red column (not a faint hollow one that reads
		// as "volume happens to be 0") under a bold red MUTED badge. The header's
		// "Vol" label also flips to a red "MUTED" so it's caught from the top too.
		col := stRed.Render("█")
		for i := 0; i < barH; i++ {
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
	cells := W - (statusW + 1 + DispW(cur) + 1 + 1 + DispW(rem))
	if cells < 1 {
		cells = 1
	}
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

// friendlyError condenses a raw ssh / network error into a short, calm line for
// the UI. The raw stderr (e.g. "ssh: Could not resolve hostname lp10.local:
// nodename nor servname provided, or not known") is accurate but long and
// alarming; these map the common cases to something human and actionable, and at
// worst just drop the "ssh:" prefix.
func friendlyError(msg string) string {
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "could not resolve") || strings.Contains(low, "name or service not known"):
		return "can't find the device — are you on the home network?"
	case strings.Contains(low, "no route to host") || strings.Contains(low, "network is unreachable"):
		return "no route to the device — check the network"
	case strings.Contains(low, "connection refused"):
		return "the device refused the connection"
	case strings.Contains(low, "timed out") || strings.Contains(low, "timeout"):
		return "connection timed out — the device may be off or away"
	case strings.Contains(low, "permission denied") || strings.Contains(low, "publickey"):
		return "ssh authentication failed"
	case strings.HasPrefix(low, "ssh: "):
		return strings.TrimSpace(msg[5:])
	}
	return msg
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
	trackW := W - sliderLabelW - sliderValW
	if trackW < 1 {
		trackW = 1
	}
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
		pad := trackW + sliderValW - 1 - 1 - DispW(state)
		if pad < 0 {
			pad = 0
		}
		return labelCell + content + strings.Repeat(" ", pad)
	}

	// Ranged: a horizontal slider ────●────
	frac := 0.0
	if known && sp.Max > sp.Min {
		frac = float64(v-sp.Min) / float64(sp.Max-sp.Min)
	}
	knobPos := int(frac*float64(trackW-1) + 0.5)
	if knobPos < 0 {
		knobPos = 0
	}
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

func (m *model) footerRow(W int) string {
	var hint string
	if m.pane == paneEQ {
		hint = "↑↓ pick · ←→ adjust · enter toggle · tab player · q quit"
	} else {
		hint = "space play · ↑↓ vol · m mute · e/tab EQ · ? diag · q quit"
	}
	return lipgloss.NewStyle().Width(W).Align(lipgloss.Right).
		Render(m.sty.sDmr.Render(Clip(hint, W)))
}

// diagCardsMinW is the inner width at/above which the diagnostics overlay uses the
// two-column card grid; below it, the single-column stacked layout (which fits a
// narrow terminal and degrades gracefully) is used instead.
const diagCardsMinW = 100

// renderDiag picks the diagnostics layout by width: a two-column card grid on a
// wide terminal (filling the space and surfacing the audio-chain metrics), the
// stacked single-column read-out when narrow.
func (m *model) renderDiag(s protocol.Snapshot, now time.Time, W int) string {
	if W >= diagCardsMinW {
		return m.renderDiagCards(s, now, W)
	}
	return m.renderDiagStacked(s, now, W)
}

func (m *model) renderDiagStacked(s protocol.Snapshot, now time.Time, W int) string {
	t := m.sty
	lastRx, dData, att, derr, si := m.st.DiagView()
	dev := m.st.DevInfoView()
	netv := m.st.NetView()
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
	case !dData.IsZero() && now.Sub(dData) > workers.SilentAfter:
		// Match the watchdog's silence threshold (not a tighter one): the device's
		// idle loop legitimately drops to a ~3s poll cadence, so a shorter window
		// would flash "LUCI silent" between healthy low-poll frames.
		hr, hrW = stWarn.Render("● LUCI silent · "+clock), DispW("● LUCI silent · "+clock)
	default:
		hr, hrW = t.sAcc.Render("●")+t.sDim.Render(" "+clock), DispW("● "+clock)
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
	if m.cfg.Discovered {
		host += " · mDNS"
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

	if dev != nil && (dev.IP != "" || dev.Net != "") {
		add(m.dividerRow("network", W))
		if dev.Net == "wifi" {
			band := ""
			if f, err := strconv.Atoi(dev.Freq); err == nil && f > 0 {
				b := " · 2.4 GHz"
				if f >= 5000 {
					b = " · 5 GHz"
				}
				band = fmt.Sprintf(" · ch %d%s", freqToChan(f), b)
			}
			add(m.diagLine("link", t.sBri.Render("wi-fi")+t.sDim.Render(" · ")+t.sTxt.Render(orDash(dev.SSID))+t.sDim.Render(band)))
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
					add(m.diagGauge("signal", t.gaugeBar(float64(dbm+90)/60, gw, pen), pen.Render(valTxt), detail))
				}
			}
		} else {
			detail := ""
			if sp, err := strconv.Atoi(dev.Speed); err == nil && sp > 0 {
				detail += fmt.Sprintf(" · %d Mbit/s", sp)
			}
			if dev.Duplex != "" {
				detail += " · " + dev.Duplex + " duplex"
			}
			add(m.diagLine("link", t.sBri.Render("ethernet")+t.sDim.Render(detail)))
		}
		add(m.diagLine("address", t.sTxt.Render(orDash(dev.IP))+t.sDim.Render(" · gw "+orDash(dev.Gateway))))
		if netv.RatesOK {
			add(m.diagLine("traffic", t.sDim.Render("rx ")+t.sTxt.Render(fmtRate(netv.RxRate))+
				t.sDim.Render(" · tx ")+t.sTxt.Render(fmtRate(netv.TxRate))))
		}
		// one row per latency target: avg · jitter · peak · a sparkline of the
		// rolling window. The sparkline fills the remaining width (its column
		// starts after the fixed numeric fields), bounded by the ring size.
		sparkW := min(pingHistory, W-latencyFixedCols)
		names := [3]string{"you", "gw", pingLabel(m.cfg.PingHost)}
		latLabel := "latency"
		for i, ps := range netv.Ping {
			if !ps.OK {
				continue
			}
			add(m.diagLine(latLabel, m.latencyRow(names[i], ps, sparkW)))
			latLabel = ""
		}
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

	// footer (and any device error) pins to the bottom; the gap fills the frame
	var tail []string
	if derr != "" {
		// prettified, not the raw ssh dump — the overlay already shows the state
		// (disconnected · tunnel down · N attempts), so keep only the readable reason
		tail = append(tail, stWarn.Render(Clip(GL["warn"]+" "+friendlyError(derr), W)), "")
	}
	tail = append(tail, t.sDmr.Render("live · any key returns to the dashboard"))

	// on a too-short pane, trim the read-out from the bottom and flag it
	if room := m.rows - 3 - len(tail); room > 2 && len(L) > room {
		L = L[:room]
		L[room-1] = t.sDmr.Render("… resize for more")
	}
	return strings.Join(frameBody(L, tail, m.rows-3, false), "\n") // top-aligned: read-out hugs the top, footer stays pinned below
}

// diagCard renders a titled rounded box: "╭─ title ─…─╮", each row framed by
// "│ … │", then "╰──…──╯" — every line exactly w display cols. Rows are pre-styled
// and right-padded to the inner width (callers keep each row ≤ w-4 wide).
func (m *model) diagCard(title string, rows []string, w int) []string {
	t := m.sty
	inner := w - 4 // │ + space … space + │
	if inner < 1 {
		inner = 1
	}
	bar := func(n int) string {
		if n < 0 {
			n = 0
		}
		return strings.Repeat(GL["h"], n)
	}
	rule := t.sDmr
	out := make([]string, 0, len(rows)+2)
	out = append(out, rule.Render(GL["tl"]+GL["h"])+" "+t.sAcc.Bold(true).Render(title)+" "+
		rule.Render(bar(w-DispW(title)-5)+GL["tr"]))
	v := rule.Render(GL["v"])
	for _, r := range rows {
		// clip then pad to the exact inner VISIBLE width (lipgloss.Width ignores the
		// ANSI in a styled row; DispW would count those bytes and skip the padding,
		// scattering the right border — the bug that broke the grid on a real terminal).
		out = append(out, v+" "+padVis(m.clipStyled(r, inner), inner)+" "+v)
	}
	return append(out, rule.Render(GL["bl"]+bar(w-2)+GL["br"]))
}

// renderDiagCards is the wide-terminal diagnostics layout: a two-column grid of
// titled cards balanced by content — left: device · connection · network (the
// static identity/connectivity side); right: audio · resources · latency (the live
// metrics). Filling the space the stacked view left empty and surfacing the
// audio-chain / contention metrics. Sparklines and gauges get full card width.
func (m *model) renderDiagCards(s protocol.Snapshot, now time.Time, W int) string {
	t := m.sty
	lastRx, dData, att, derr, si := m.st.DiagView()
	dev := m.st.DevInfoView()
	netv := m.st.NetView()
	eqConn, eqv := m.st.EQView()

	lo := func(v, a, b float64) lipgloss.Style { // lower-is-better health picker
		switch {
		case v < a:
			return t.sAcc
		case v < b:
			return stWarn
		default:
			return stRed
		}
	}

	cardWL := (W - 2) / 2 // two cards + a 2-col gutter span W
	cardWR := W - 2 - cardWL
	innerL, innerR := cardWL-4, cardWR-4
	const gwc = 12 // gauge width inside a card

	// row builders (label column + value), clipped/padded to a card's inner width.
	kvP := func(inner int, label, value string, pen lipgloss.Style) string {
		return t.sDim.Render(label) + labelGap(label, diagLabelW) +
			pen.Render(Clip(value, inner-diagLabelW))
	}
	kvR := func(label, styled string) string { // pre-styled value; caller fits width
		return t.sDim.Render(label) + labelGap(label, diagLabelW) + styled
	}
	cg := func(inner int, label, valuePlain string, frac float64, pen lipgloss.Style, detail string) string {
		out := t.sDim.Render(label) + labelGap(label, diagLabelW) +
			t.gaugeBar(frac, gwc, pen) + "  " + pen.Render(valuePlain)
		if detail != "" {
			if d := Clip(detail, inner-(diagLabelW+gwc+2+DispW(valuePlain))-1); d != "" {
				out += " " + t.sDmr.Render(d)
			}
		}
		return out
	}

	// ---- device card ----
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
	if m.cfg.Discovered {
		host += " · mDNS"
	}
	deviceCard := m.diagCard("device", []string{
		kvP(innerL, "host", host, t.sTxt),
		kvP(innerL, "device", model, t.sTxt),
		kvP(innerL, "os", os+" · "+cores+" cores", t.sTxt),
		kvP(innerL, "firmware", fw, t.sTxt),
		kvP(innerL, "build", build, t.sTxt),
		kvP(innerL, "uptime", up, t.sTxt),
		kvP(innerL, "mac", mac, t.sTxt),
	}, cardWL)

	// ---- connection card ----
	rxTxt, rxPen := "—", t.sDim
	if !lastRx.IsZero() {
		secs := now.Sub(lastRx).Seconds()
		rxTxt, rxPen = fmt.Sprintf("%.1fs", secs), lo(secs, 3, 8)
	}
	attWord := "attempts"
	if att == 1 {
		attWord = "attempt"
	}
	tunTxt, tunPen := "down", stRed
	if eqConn {
		tunTxt, tunPen = "live", t.sAcc
	}
	connCard := m.diagCard("connection", []string{
		kvR("player", t.sTxt.Render("ssh · rx ")+rxPen.Render(rxTxt)+t.sTxt.Render(fmt.Sprintf(" ago · %d %s", att, attWord))),
		kvR("control", t.sTxt.Render("tunnel :2018 · ")+tunPen.Render(tunTxt)),
	}, cardWL)

	leftLines := append(deviceCard, connCard...)

	// latency is a LIVE metric (like cpu/temp), so it rides the right column with
	// audio + resources — which also balances the two columns (device/connection/
	// network are the static, identity side on the left).
	var latencyCard []string

	// ---- network card (left) ----
	if dev != nil && (dev.IP != "" || dev.Net != "") {
		var nrows []string
		if dev.Net == "wifi" {
			band := ""
			if f, err := strconv.Atoi(dev.Freq); err == nil && f > 0 {
				b := " · 2.4 GHz"
				if f >= 5000 {
					b = " · 5 GHz"
				}
				band = fmt.Sprintf(" · ch %d%s", freqToChan(f), b)
			}
			nrows = append(nrows, kvR("link", t.sBri.Render("wi-fi")+t.sDim.Render(" · ")+t.sTxt.Render(orDash(dev.SSID))+t.sDim.Render(band)))
			if si != nil {
				if dbm, err := strconv.Atoi(si.SignalDBm); err == nil {
					pen := lo(float64(-dbm), 60, 72)
					detail := ""
					if nz, e := strconv.Atoi(si.NoiseDBm); e == nil && nz < 0 {
						detail = fmt.Sprintf("snr %d dB", dbm-nz) // signal − noise
					} else if lq, e := strconv.Atoi(si.LinkQ); e == nil && lq > 0 {
						detail = fmt.Sprintf("link %d/70", lq)
					}
					nrows = append(nrows, cg(innerL, "signal", fmt.Sprintf("%d dBm", dbm), float64(dbm+90)/60, pen, detail))
				}
			}
			if dev.Rate != "" {
				nrows = append(nrows, kvP(innerL, "rate", dev.Rate+" Mbit/s", t.sTxt))
			}
		} else {
			detail := ""
			if sp, err := strconv.Atoi(dev.Speed); err == nil && sp > 0 {
				detail += fmt.Sprintf(" · %d Mbit/s", sp)
			}
			if dev.Duplex != "" {
				detail += " · " + dev.Duplex + " duplex"
			}
			nrows = append(nrows, kvR("link", t.sBri.Render("ethernet")+t.sDim.Render(detail)))
		}
		nrows = append(nrows, kvR("address", t.sTxt.Render(orDash(dev.IP))+t.sDim.Render(" · gw "+orDash(dev.Gateway))))
		if dev.DNS != "" {
			nrows = append(nrows, kvP(innerL, "dns", dev.DNS, t.sTxt))
		}
		if netv.RatesOK {
			nrows = append(nrows, kvR("traffic", t.sDim.Render("rx ")+t.sTxt.Render(fmtRate(netv.RxRate))+t.sDim.Render(" · tx ")+t.sTxt.Render(fmtRate(netv.TxRate))))
		}
		leftLines = append(leftLines, m.diagCard("network", nrows, cardWL)...)

		// latency rows (built here while netv is in hand) — wide sparkline per target;
		// the card itself is placed in the RIGHT column below, sized to cardWR.
		var lrows []string
		sw := innerR - 19 // name(6)+avg(6)+1+jit(5)+1
		if sw < 4 {
			sw = 4
		}
		names := [3]string{"you", "gw", pingLabel(m.cfg.PingHost)}
		for i, ps := range netv.Ping {
			if !ps.OK {
				continue
			}
			peakPen := t.sDmr
			if ps.Peak > ps.Avg*2 && ps.Peak-ps.Avg > 10 {
				peakPen = stWarn
			}
			row := t.sDim.Render(padDisp(names[i], 6)) +
				t.sTxt.Render(rpadDisp(fmtLatencyMs(ps.Avg)+"ms", 6)) + " " +
				peakPen.Render(padDisp("±"+fmtLatencyMs(ps.Jitter), 5)) + " " +
				t.sDim.Render(sparkline(ps.Series, sw))
			lrows = append(lrows, row)
		}
		if len(lrows) > 0 {
			latencyCard = m.diagCard("latency", lrows, cardWR)
		}
	}

	// ---- audio card ----
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
	arows := []string{kvP(innerR, "stream", formatTxt, t.sTxt)}
	if si != nil && si.DacRate != "" {
		rate := si.DacRate
		if hz, err := strconv.Atoi(si.DacRate); err == nil {
			rate = fmtKHz(hz)
		}
		parts := []string{rate}
		if si.DacFmt != "" {
			parts = append(parts, si.DacFmt)
		}
		if si.DacCh != "" {
			parts = append(parts, si.DacCh+"ch")
		}
		dac := t.sTxt.Render(strings.Join(parts, " · "))
		if si.PcmState == "RUNNING" {
			dac += t.sAcc.Render(" ● live")
		}
		arows = append(arows, kvR("dac", dac))
	}
	if si != nil && si.BufAvail != "" && si.BufSize != "" {
		if av, e1 := strconv.Atoi(si.BufAvail); e1 == nil {
			if bs, e2 := strconv.Atoi(si.BufSize); e2 == nil && bs > 0 {
				fill := float64(bs-av) / float64(bs) // queued frames / ring size
				if fill < 0 {
					fill = 0
				}
				pen := stRed // buffer health is inverted: a FULL ring is healthy
				switch {
				case fill >= 0.5:
					pen = t.sAcc
				case fill >= 0.25:
					pen = stWarn
				}
				arows = append(arows, cg(innerR, "buffer", fmt.Sprintf("%d%%", int(fill*100+0.5)), fill, pen, "ring"))
			}
		}
	}
	volPen, volTxt := t.sAcc, fmt.Sprintf("%d%%", s.Vol)
	if s.Muted {
		volPen, volTxt = stRed, "MUTED"
	}
	arows = append(arows, cg(innerR, "volume", volTxt, float64(s.Vol)/100, volPen, ""))
	arows = append(arows, kvR("eq", m.clipStyled(m.eqReadout(eqv), innerR-diagLabelW)))
	rightLines := m.diagCard("audio", arows, cardWR)

	// ---- resources card ----
	var rrows []string
	if si != nil {
		loads := strings.Fields(si.Load)
		nc, _ := strconv.Atoi(si.NCPU)
		if nc < 1 {
			nc = 1
		}
		if len(loads) >= 1 {
			if l1, err := strconv.ParseFloat(loads[0], 64); err == nil {
				frac := l1 / float64(nc)
				detail := "1m " + loads[0]
				if si.CpuKHz != "" {
					if khz, e := strconv.Atoi(si.CpuKHz); e == nil {
						detail += fmt.Sprintf(" · %d MHz", khz/1000)
					}
				}
				rrows = append(rrows, cg(innerR, "cpu", fmt.Sprintf("%d%%", int(frac*100+0.5)), frac, lo(frac*100, 60, 85), detail))
			}
		}
		if si.Procs != "" {
			if run, tot, ok := strings.Cut(si.Procs, "/"); ok {
				rrows = append(rrows, kvR("tasks", t.sTxt.Render(run)+t.sDim.Render(" running · ")+t.sTxt.Render(tot)+t.sDim.Render(" total")))
			}
		}
		av, e1 := strconv.Atoi(si.Avail)
		tot, e2 := strconv.Atoi(si.Total)
		if e1 == nil && e2 == nil && tot > 0 {
			uf := float64(tot-av) / float64(tot)
			rrows = append(rrows, cg(innerR, "memory", fmt.Sprintf("%d%%", int(uf*100+0.5)), uf, lo(uf*100, 70, 88), fmt.Sprintf("%d/%d MB free", av/1024, tot/1024)))
		}
		if mc, err := strconv.Atoi(si.TempmC); err == nil {
			c := mc / 1000
			rrows = append(rrows, cg(innerR, "temp", fmt.Sprintf("%d °C", c), float64(c)/85, lo(float64(c), 60, 75), "SoC"))
		}
	}
	if dev != nil {
		gauge := func(label string, used, total string, a, b float64, suffix string) {
			u, e1 := strconv.Atoi(used)
			tt, e2 := strconv.Atoi(total)
			if e1 == nil && e2 == nil && tt > 0 {
				uf := float64(u) / float64(tt)
				rrows = append(rrows, cg(innerR, label, fmt.Sprintf("%d%%", int(uf*100+0.5)), uf, lo(uf*100, a, b), fmt.Sprintf("%d/%d MB %s", u/1024, tt/1024, suffix)))
			}
		}
		gauge("data", dev.DataUsed, dev.DataTotal, 80, 92, "/lsync")
	}
	rightLines = append(rightLines, m.diagCard("resources", rrows, cardWR)...)
	rightLines = append(rightLines, latencyCard...) // live metric, balances the columns

	// ---- title + zip the two columns ----
	clock := now.Format("15:04")
	var hr string
	var hrW int
	switch {
	case !s.Connected:
		hr, hrW = stWarn.Render("● disconnected"), DispW("● disconnected")
	case !dData.IsZero() && now.Sub(dData) > workers.SilentAfter:
		hr, hrW = stWarn.Render("● LUCI silent · "+clock), DispW("● LUCI silent · "+clock)
	default:
		hr, hrW = t.sAcc.Render("●")+t.sDim.Render(" "+clock), DispW("● "+clock)
	}
	content := []string{between(t.sAcc.Bold(true).Render("diagnostics"), DispW("diagnostics"), hr, hrW, W), ""}
	gutter := "  " // 2-col gutter: cardWL + 2 + cardWR == W
	blankR := strings.Repeat(" ", cardWR)
	n := len(leftLines)
	if len(rightLines) > n {
		n = len(rightLines)
	}
	for i := 0; i < n; i++ {
		l := strings.Repeat(" ", cardWL)
		if i < len(leftLines) {
			l = padVis(leftLines[i], cardWL)
		}
		r := blankR
		if i < len(rightLines) {
			r = padVis(rightLines[i], cardWR)
		}
		content = append(content, l+gutter+r)
	}

	var tail []string
	if derr != "" {
		tail = append(tail, stWarn.Render(Clip(GL["warn"]+" "+friendlyError(derr), W)), "")
	}
	tail = append(tail, t.sDmr.Render("live · any key returns to the dashboard"))
	return strings.Join(frameBody(content, tail, m.rows-3, false), "\n")
}

// fmtKHz renders a sample rate in kHz: "44.1 kHz", "48 kHz", "96 kHz".
func fmtKHz(hz int) string {
	if hz%1000 == 0 {
		return strconv.Itoa(hz/1000) + " kHz"
	}
	return strconv.FormatFloat(float64(hz)/1000, 'f', 1, 64) + " kHz"
}

// rpadDisp left-pads s with spaces to display width w (right-justify); no-op if ≥ w.
func rpadDisp(s string, w int) string {
	if d := w - DispW(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

var ansiSGR = regexp.MustCompile("\x1b\\[[0-9;]*m")

// clipStyled clips an already-styled string to display width w by stripping it,
// clipping the plain text, and re-dimming — used where a styled readout (eq) must
// fit a card cell and exact per-segment colour isn't worth preserving on overflow.
func (m *model) clipStyled(styled string, w int) string {
	if lipgloss.Width(styled) <= w {
		return styled
	}
	return m.sty.sDim.Render(Clip(ansiSGR.ReplaceAllString(styled, ""), w))
}

// gridRow renders a two-column "label value | label value" row, exactly W wide.
func (m *model) gridRow(k1, v1, k2, v2 string, W int) string {
	half := W / 2
	return m.cellKV(k1, v1, half) + m.cellKV(k2, v2, W-half)
}

func (m *model) cellKV(k, v string, w int) string {
	const labW = 9
	vv := Clip(v, w-labW)
	out := m.sty.sDim.Render(k) + labelGap(k, labW) + m.sty.sTxt.Render(vv)
	if vis := labW + DispW(vv); vis < w {
		out += strings.Repeat(" ", w-vis)
	}
	return out
}

// diagLine renders "label  value" with a fixed dim label column.
func (m *model) diagLine(label, value string) string {
	return m.sty.sDim.Render(label) + labelGap(label, diagLabelW) + value
}

// diagGauge renders "label  [gauge]  value detail".
func (m *model) diagGauge(label, gauge, value, detail string) string {
	return m.sty.sDim.Render(label) + labelGap(label, diagLabelW) +
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

// fmtRate renders a bytes/sec throughput in the largest unit that keeps it ≥1.
func fmtRate(bps float64) string {
	switch {
	case bps >= 1<<20:
		return fmt.Sprintf("%.1f MB/s", bps/(1<<20))
	case bps >= 1<<10:
		return fmt.Sprintf("%.0f KB/s", bps/(1<<10))
	default:
		return fmt.Sprintf("%.0f B/s", bps)
	}
}

// fmtLatencyMs renders a millisecond latency with one decimal under 10ms (sub-ms
// LAN hops would otherwise round to a meaningless "0"), whole numbers above.
// (Distinct from FmtMs(int), which formats a track position as MM:SS.)
func fmtLatencyMs(ms float64) string {
	if ms < 10 {
		return fmt.Sprintf("%.1f", ms)
	}
	return fmt.Sprintf("%.0f", ms)
}

const (
	// diagLabelW is the dim label column shared by every diagnostics row (see
	// diagLine / diagGauge): the label, left-padded to this width, then the value.
	diagLabelW = 10

	// The latency row's fixed fields, in render order (see latencyRow); the
	// sparkline takes whatever width remains. latencyFixedCols is computed from
	// these so the columns and the sparkline stay aligned when a field width
	// changes — TestLatencyRowSparklineColumnMatchesFixedCols pins the start.
	latNameW   = 8     // target name (left-padded)
	latAvgW    = 4     // average ms (right-aligned), before its unit
	latAvgUnit = " ms" // the avg field's trailing unit
	latJitW    = 5     // ±jitter
	latPeakW   = 8     // "max <peak>"

	// label + name + avg + unit + jitter + peak, plus the three single-space field
	// separators (after avg, after jitter, after peak).
	latencyFixedCols = diagLabelW + latNameW + latAvgW + len(latAvgUnit) + latJitW + latPeakW + 3
	pingHistory      = 30
)

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// sparkline renders the values as block glyphs scaled to the window's own
// min/max — a flat baseline reads low and a transient spike stands tall — using
// the last maxW samples (so it shows the most recent history when space is tight).
func sparkline(vals []float64, maxW int) string {
	if maxW <= 0 || len(vals) == 0 {
		return ""
	}
	if len(vals) > maxW {
		vals = vals[len(vals)-maxW:]
	}
	lo, hi := vals[0], vals[0]
	for _, v := range vals {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	span := hi - lo
	// Floor the span relative to the peak so a steady signal with a little jitter
	// (latency that barely moves) reads as a calm low band instead of amplifying
	// sub-millisecond noise into a full-height jagged mess. A real spike still
	// exceeds the floor and towers over the baseline.
	if floor := hi * 0.8; span < floor {
		span = floor
	}
	var b strings.Builder
	for _, v := range vals {
		idx := 0
		if span > 0 {
			idx = int((v-lo)/span*float64(len(sparkRunes)-1) + 0.5)
		}
		if idx >= len(sparkRunes) {
			idx = len(sparkRunes) - 1
		}
		b.WriteRune(sparkRunes[idx])
	}
	return b.String()
}

// latencyRow renders one target — name, average, jitter, peak (amber once a real
// spike has landed), and the sparkline. The numeric fields are fixed-width so the
// sparkline column lines up across the three rows.
func (m *model) latencyRow(name string, ps protocol.PingStat, sparkW int) string {
	t := m.sty
	pad := func(s string, w int) string {
		if d := w - DispW(s); d > 0 {
			return s + strings.Repeat(" ", d)
		}
		return s
	}
	rpad := func(s string, w int) string {
		if d := w - DispW(s); d > 0 {
			return strings.Repeat(" ", d) + s
		}
		return s
	}
	peakPen := t.sDmr
	if ps.Peak > ps.Avg*2 && ps.Peak-ps.Avg > 10 { // a genuine spike, not baseline wobble
		peakPen = stWarn
	}
	return t.sDim.Render(pad(name, latNameW)) +
		t.sTxt.Render(rpad(fmtLatencyMs(ps.Avg), latAvgW)+latAvgUnit) + " " +
		t.sDmr.Render(pad("±"+fmtLatencyMs(ps.Jitter), latJitW)) + " " +
		peakPen.Render(pad("max "+fmtLatencyMs(ps.Peak), latPeakW)) + " " +
		t.sDim.Render(sparkline(ps.Series, sparkW)) // dim: a subtle inline trend, not a glare
}

// pingLabel shortens the configured internet target for the latency row: an IP
// is shown whole, a hostname collapses to its second-level domain
// (apresolve.spotify.com → spotify).
func pingLabel(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return "net"
	}
	parts := strings.Split(host, ".")
	if _, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
		return host // numeric final label → an IPv4 address; show it whole
	}
	if len(parts) >= 2 {
		return parts[len(parts)-2]
	}
	return host
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

// labelGap is the space run after a fixed-width diagnostics label: the column
// width minus the label's display width, floored at 0 so a label wider than its
// column can never produce a negative (panicking) repeat count.
func labelGap(label string, col int) string {
	return strings.Repeat(" ", max(0, col-DispW(label)))
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
	go workers.ArtWorker(st, cfg)

	m := newModel(st, cfg, cmds, eqcmds)
	opts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithoutSignalHandler()}
	if cfg.Mouse {
		// Capture clicks/drag/scroll for the transport, volume rail, and EQ bands.
		// CellMotion (not AllMotion) reports motion only while a button is held, so
		// a left-drag scrubs a control while idle motion stays out of the input loop.
		opts = append(opts, tea.WithMouseCellMotion())
	}
	p := tea.NewProgram(m, opts...)

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
