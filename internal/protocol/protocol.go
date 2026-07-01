// Package protocol implements the LUCI wire protocol: record framing (records.go),
// MB42 parsing (mb42.go), sanitization at the parse boundary (sanitize.go),
// record application (apply.go), command reduction and the write whitelist
// (commands.go), the diagnostics network readout (netstat.go), and the shared
// State that worker goroutines mutate and the TUI reads (state.go).
// Port of lp10lib/protocol.py.
package protocol

import "time"

// Echo-suppression / debounce windows.
const (
	VolHoldDuration  = 2500 * time.Millisecond
	PlayHoldDuration = 1500 * time.Millisecond
	DebounceWindow   = 3 * time.Second
	// EQHoldDuration suppresses the device's own broadcast echo of an EQ/tone
	// control just changed locally, so a rapid drag isn't fought by the echo.
	EQHoldDuration = 600 * time.Millisecond
)
