// Keyboard input: normalization of Bubble Tea key messages and the pane-aware
// dispatch of each keypress.

package tui

import tea "github.com/charmbracelet/bubbletea"

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
