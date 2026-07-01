package transport

import (
	_ "embed"
	"strings"
)

// remoteLoopScript is the on-device BusyBox-ash streaming loop. It is authored as
// readable, commented shell in remote_loop.src.sh and minified into remote_loop.sh
// (this embed) by `go generate ./internal/transport` — edit the .src.sh, never the
// generated one-liner (TestEmbeddedLoopMatchesSource guards against a stale embed).
// Collapsing to a single comment-free line keeps the on-wire footprint minimal (it
// shares the device with playback). RemoteLoop substitutes two placeholders at spawn:
// the ph='__PING_HOST__' diagnostics ping target, and the __MIDS__ command-id
// whitelist spliced at `case "$mid" in`. The contract doc lives here.
//
// See lp10lib/transport.remote_loop for the full rationale (timing-based EOF
// detection, adaptive idle cadence, two-step burst drain). It descends from the
// Python version's loop; the Go port additionally emits four one-shot prologue
// records before the loop: an @@i static device/network block (the active
// interface, its link details, the FriendlyName from reg 90, the data-partition
// usage, and the resolver), an @@c capability block (which streaming services
// are enabled — running daemons via pidof, env-gated features via getenv — for
// the config view), and the raw @@d device-details (reg 92: serial / MACs / MCU
// + full firmware version) and @@g multiroom-group (reg 39) register reads,
// parsed laptop-side. It appends
// SoC temp, interface byte counters, Wi-Fi signal/link/noise, three ICMP ping RTTs
// (laptop / gateway / internet), the ALSA audio chain (playback state, the DAC's
// actual rate/format/channels, and the buffer fill from avail/buffer_size), the
// current CPU clock, the running/total process count, and the interface
// error/drop counters to each @@s line, for the diagnostics panel. Every added
// field defaults to "-" so hardware that lacks a path (no /proc/asound, a
// fixed-clock CPU) degrades gracefully rather than breaking the positional
// line. (xruns and rootfs% were dropped after an on-device probe: the
// AR241CE's status file carries no xruns line, and / is a read-only squashfs
// pinned at 100% — the writable space is /lsync, already reported.)
//
// Footprint is kept minimal — it shares the device with playback:
//   - the resource stats (@@s: load/mem/temp/throughput/latency) are gathered and
//     emitted ONLY while the diagnostics overlay is open. The TUI flips that with the
//     MID-90 control message (1 = on, 0 = off) on the same stdin channel as the
//     playback commands; the device defaults the flag off and resets it on every
//     connection, so the TUI re-asserts while the overlay is open. Off the
//     overlay each tick does zero /proc stat reads. The toggle never reaches
//     LUCI_local and skips the per-keypress track re-read.
//   - per tick the only forks are the LUCI_local device-API reads, and even
//     those are trimmed: play-state (-r 51) and volume (-r 64) stay per-tick
//     (they're data-bearing for the watchdog and must reflect external changes
//     promptly), but position (-r 49) is polled only every 3rd tick (pc49 gate)
//     since the TUI extrapolates position locally and only needs a periodic
//     resync — any command, play-state flip, or detected track skip forces an
//     immediate re-read. The per-tick position/play values use echo (a builtin)
//     rather than printf (an applet on some BusyBox builds). Every stat comes
//     from /proc and /sys via shell builtins (no awk/sed/grep), and the meminfo
//     and /proc/net/wireless scans break as soon as their fields are found, so
//     the timing-based EOF detection stays cheap and undisturbed. The exception
//     is latency: while the overlay is open the three `ping`s (the only per-tick
//     execs beyond LUCI_local) run on every 3rd @@s — gated by pgc, mirroring the
//     pc49 position poll — each capped at -W1 and parsed by parameter expansion in
//     the pg() helper, which returns via the shared $o (no capturing subshell, so
//     each call forks once — for ping — not twice). The gate bounds the per-tick
//     stall (an unreachable target
//     costs a full -W1 second) so a dead target can't lag playback updates on every
//     tick; the intervening ticks emit "-", which the parser folds in as "no new
//     sample" rather than a spike.
//   - the once-per-connection @@i probes select the active interface from the
//     default route and parse iw / ip-route / sysfs and fwVersion.conf with shell
//     parameter expansion rather than sed|head pipelines, sparing ~a dozen
//     fork+execs at connect/reconnect.
//   - the once-per-connection @@c capability block reads the streaming-service
//     state (pidof for the running daemons, getenv for the env-gated features) —
//     a read-only burst paid once at connect, not per tick, so the config view
//     paints the moment it's opened without any further device work. The pr/gv
//     helpers print "key=value" directly (one exec per service, no capturing
//     subshell), so the whole block is ~8 forks, not 16.
//
//go:generate go run mkloop.go
//go:embed remote_loop.sh
var remoteLoopScript string

// RemoteLoop returns the on-device loop script with the given command-id whitelist
// (default "40|64") and the diagnostics internet-ping target. The ping host is
// sanitized then substituted first: sanitizeHost strips quotes and underscores, so a
// hostile value can neither break out of the single-quoted ph assignment nor forge
// the __MIDS__ token. mids is trusted internal input. Each placeholder occurs once.
func RemoteLoop(mids, pingHost string) string {
	if mids == "" {
		mids = "40|64"
	}
	s := strings.Replace(remoteLoopScript, "__PING_HOST__", sanitizeHost(pingHost), 1)
	return strings.Replace(s, "__MIDS__", mids, 1)
}

// sanitizeHost keeps only hostname/IP-safe characters so a user-supplied
// ping_host can be embedded in the device loop without shell escaping; an empty
// or fully-stripped value falls back to the default target.
func sanitizeHost(h string) string {
	var b strings.Builder
	for _, r := range h {
		if r == '.' || r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "spotify.com"
	}
	return b.String()
}
