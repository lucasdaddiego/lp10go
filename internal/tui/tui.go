// Program lifecycle: Run wires State, the worker goroutines, the media-key tap,
// and the Bubble Tea program together, and tears them down on exit. The model
// itself lives in model.go; see display.go for the package doc.

package tui

import (
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lucasdaddiego/lp10/internal/config"
	"github.com/lucasdaddiego/lp10/internal/mediakey"
	"github.com/lucasdaddiego/lp10/internal/protocol"
	"github.com/lucasdaddiego/lp10/internal/tunnel"
	"github.com/lucasdaddiego/lp10/internal/workers"
)

// preloadSnapshot seeds State from a cached snapshot for an instant first paint.
func preloadSnapshot(st *protocol.State, cached map[string]any) {
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
	if eq, ok := cached["eq"].(map[string]any); ok {
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

	// Media transport keys (macOS): drive the device from the keyboard's
	// play/pause, next, and prev even when lp10 isn't focused. The tap only
	// consumes the keys while connected, so they pass through to other apps when
	// the device is away. No-op on non-macOS; best-effort if the tap can't be
	// installed (Accessibility not granted) — note it and carry on.
	stopKeys, keyErr := mediakey.Start(mediakey.Config{
		Connected: func() bool { return st.Snap().Connected },
		OnKey: func(k mediakey.Key) {
			if action, ok := keyToAction(k); ok {
				p.Send(mediaKeyMsg{action: action})
			}
		},
		// Fires only when the tap re-arms after an earlier denial (Accessibility
		// granted mid-session), confirming the keys are now live.
		OnActive: func() { st.Note("media keys on") },
	})
	if keyErr != nil {
		st.Note("media keys off — " + keyErr.Error())
	}
	defer stopKeys()

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
