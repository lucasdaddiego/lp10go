// The message loop and controller actions: tick cadences, Update, and the
// send/do verbs that turn input into device commands.

package tui

import (
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/mediakey"
	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// Two cadences drive the UI. The logic tick (100ms) advances the marquee, the
// stats keep-alive, and the window title — its constants (marqueeColTicks,
// StatsReassertTicks) are counted in these 100ms units. The frame tick is the
// animation clock, decoupled so the plasma motif and the extrapolated seek bar
// glide at ~30fps on a GPU terminal without speeding up the logic above. It
// idles to a gentle rate while paused/idle, when the motif is frozen and the
// motif cache makes those wake-ups nearly free.
type logicMsg struct{}
type frameMsg struct{}

// mediaKeyMsg carries a macOS media transport key captured by the background
// event tap (internal/mediakey). It is delivered through the program so the
// action runs on the update loop — the tap thread must never touch model state.
type mediaKeyMsg struct{ action string }

// keyToAction maps a captured media key to the transport action do() understands.
func keyToAction(k mediakey.Key) (action string, ok bool) {
	switch k {
	case mediakey.PlayPause:
		return "toggle", true
	case mediakey.Next:
		return "next", true
	case mediakey.Prev:
		return "prev", true
	}
	return "", false
}

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
		// One PREV; on Spotify the device restarts the current track first, so
		// skipping back is a double-press (device behavior, not modeled here).
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
	case mediaKeyMsg:
		m.do(msg.action)
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
