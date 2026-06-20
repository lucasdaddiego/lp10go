package tui

import (
	"strings"
	"testing"

	"github.com/lucasdaddiego/lp10go/internal/protocol"
	"github.com/lucasdaddiego/lp10go/internal/tunnel"
	"github.com/lucasdaddiego/lp10go/internal/workers"
)

func eqModel(t *testing.T) (*model, *protocol.State, chan workers.EQCommand) {
	t.Helper()
	st := protocol.NewState()
	st.ApplyTunnel("MXV", 40)
	st.ApplyTunnel("EQS", 0)
	eqcmds := make(chan workers.EQCommand, 16)
	m := newModel(st, defaultCfg(), make(chan *protocol.Command, 8), eqcmds)
	m.rows, m.cols = 24, 80
	return m, st, eqcmds
}

func TestEQPaneFocusAdjustToggle(t *testing.T) {
	m, st, eqcmds := eqModel(t)

	// 'e' focuses the EQ pane; first display slot is the EQ switch (EQS, a toggle).
	m.key(kr('e'))
	if m.pane != paneEQ || m.eqFocus != 0 {
		t.Fatalf("after e: pane=%d focus=%d", m.pane, m.eqFocus)
	}
	if m.eqSpec().Code != "EQS" {
		t.Fatalf("display slot 0 is %s, want EQS", m.eqSpec().Code)
	}

	// enter flips the EQ toggle 0 -> 1, optimistic + queued.
	m.key(ke(kEnter))
	if v, _ := st.EQValue("EQS"); v != 1 {
		t.Errorf("EQS=%d want 1", v)
	}
	if cmd := <-eqcmds; cmd.Code != "EQS" || cmd.Val != 1 {
		t.Errorf("queued cmd=%+v want {EQS 1}", cmd)
	}

	// Max Vol (MXV) is the last display slot; up bumps it by its step (5): 40 -> 45.
	m.eqFocus = len(eqOrder) - 1
	if m.eqSpec().Code != "MXV" {
		t.Fatalf("last display slot is %s, want MXV", m.eqSpec().Code)
	}
	m.key(ke(kUp))
	if v, _ := st.EQValue("MXV"); v != 45 {
		t.Errorf("MXV=%d want 45", v)
	}
	if cmd := <-eqcmds; cmd.Code != "MXV" || cmd.Val != 45 {
		t.Errorf("queued cmd=%+v want {MXV 45}", cmd)
	}

	// esc steps back to the player rather than quitting.
	if m.key(ke(kEsc)) != "" {
		t.Error("esc in EQ pane should not drain/quit")
	}
	if m.pane != paneNow {
		t.Error("esc should return focus to the now-playing pane")
	}
}

func TestTabSwitchesPane(t *testing.T) {
	m, _, _ := eqModel(t)
	m.key(ke(kTab))
	if m.pane != paneEQ {
		t.Fatalf("tab should switch to EQ pane, got %d", m.pane)
	}
	m.key(ke(kTab))
	if m.pane != paneNow {
		t.Fatalf("tab should switch back, got %d", m.pane)
	}
}

func TestEQClampsAtMin(t *testing.T) {
	m, st, eqcmds := eqModel(t)
	st.ApplyTunnel("MXV", 0)
	m.key(kr('e'))
	m.eqFocus = len(eqOrder) - 1 // Max Vol is the last display slot
	m.key(ke(kDown))             // already 0 -> clamps
	if v, _ := st.EQValue("MXV"); v != 0 {
		t.Errorf("MXV=%d want 0 (clamped)", v)
	}
	if cmd := <-eqcmds; cmd.Val != 0 {
		t.Errorf("queued val=%d want 0", cmd.Val)
	}
}

func TestEQDisplayOrder(t *testing.T) {
	// EQ + tone, then deep bass, then the rarely-touched output cap (Max Vol) last.
	want := []string{"EQS", "TRE", "MID", "BAS", "VBS", "VBI", "MXV"}
	if len(eqOrder) != len(want) {
		t.Fatalf("eqOrder len=%d want %d", len(eqOrder), len(want))
	}
	for d, code := range want {
		if got := tunnel.Specs[eqOrder[d]].Code; got != code {
			t.Errorf("display slot %d = %s, want %s", d, got, code)
		}
	}
}

func TestDashboardRenders(t *testing.T) {
	m, _, _ := eqModel(t)
	protocol.ApplyRecord(m.st, playingRecord())
	out := m.View()
	for _, want := range []string{"equalizer", "Max", "Treble", "Mid", "Bass"} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard render missing %q", want)
		}
	}
}
