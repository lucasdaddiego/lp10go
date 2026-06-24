// Package transport handles SSH: Keychain/askpass auth, the ssh argv, the
// on-device streaming loop, and stderr classification. Port of
// lp10lib/transport.py.
package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/lucasdaddiego/lp10go/internal/config"
)

const (
	AskpassEnv   = "LP10_ASKPASS"
	MarkerNoItem = "lp10-askpass: no-item"
	MarkerLocked = "lp10-askpass: keychain-locked"
	MarkerBroken = "lp10-askpass: security-failed"
	// KeychainHint: -w with no value makes security(1) prompt interactively,
	// so the password never lands in shell history or `ps` output.
	KeychainHint = "security add-generic-password -U -a root -s lp10 -w"
)

// TransportError carries a fatal flag and a retry cadence, mirroring the Python
// TransportError raised below main().
type TransportError struct {
	Msg     string
	Fatal   bool
	Cadence time.Duration
}

func (e *TransportError) Error() string { return e.Msg }

// secOutcome is the result of invoking security(1): a non-zero rc, a timeout, or
// an inability to run it at all (the OSError class).
type secOutcome struct {
	stdout, stderr string
	rc             int
	timeout        bool
	runErr         error // could not execute security(1)
}

// runSecurity invokes security(1); overridable in tests.
var runSecurity = realRunSecurity

func realRunSecurity() secOutcome {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "security", "find-generic-password",
		"-a", "root", "-s", "lp10", "-w")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	o := secOutcome{stdout: out.String(), stderr: errb.String()}
	if err != nil {
		// Only a timeout if the deadline actually interrupted the process: a
		// success landing right at the deadline (err == nil) must return the
		// password, matching Python's TimeoutExpired-only semantics.
		if ctx.Err() == context.DeadlineExceeded {
			o.timeout = true
			return o
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			o.rc = exitErr.ExitCode()
		} else { // security(1) missing/not executable
			o.runErr = err
		}
	}
	return o
}

// KeychainPassword reads the LP10 password from the macOS login Keychain.
func KeychainPassword() (string, error) {
	o := runSecurity()
	if o.timeout {
		return "", &TransportError{MarkerLocked, true, 60 * time.Second}
	}
	if o.runErr != nil {
		return "", &TransportError{fmt.Sprintf("%s: %v", MarkerBroken, o.runErr), true, 60 * time.Second}
	}
	if o.rc != 0 {
		if strings.Contains(o.stderr, "could not be found") {
			return "", &TransportError{MarkerNoItem, true, 10 * time.Second}
		}
		return "", &TransportError{MarkerLocked, true, 60 * time.Second}
	}
	return strings.TrimSuffix(o.stdout, "\n"), nil
}

// AskpassMain answers ssh's password prompt from the Keychain. Failure markers
// go to stderr (shared with the parent's ssh stderr pipe); it exits the process.
func AskpassMain() {
	pw, err := KeychainPassword()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	fmt.Println(pw)
	os.Exit(0)
}

// SSHArgv builds the ssh command (binary overridable via LP10_SSH for tests).
func SSHArgv(cfg config.Config) []string {
	binary := os.Getenv("LP10_SSH")
	if binary == "" {
		binary = "ssh"
	}
	return []string{binary, "-F", "/dev/null", "-T",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "StrictHostKeyChecking=no",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=3",
		"-o", "ServerAliveInterval=20",
		"-o", "ServerAliveCountMax=3",
		"-o", "NumberOfPasswordPrompts=1",
		"-o", "PreferredAuthentications=password",
		"-o", "IdentityAgent=none",
		fmt.Sprintf("%s@%s", cfg.User, cfg.Host)}
}

// SpawnEnv returns the child environment: ssh re-execs this binary as
// SSH_ASKPASS on every connection attempt, with LP10_ASKPASS=1 set so it takes
// the askpass hot path.
func SpawnEnv() []string {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	overrides := map[string]string{
		"SSH_ASKPASS":         exe,
		"SSH_ASKPASS_REQUIRE": "force",
		AskpassEnv:            "1",
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, kv := range os.Environ() {
		k := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			k = kv[:i]
		}
		if _, ok := overrides[k]; !ok {
			env = append(env, kv)
		}
	}
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}

// remoteLoopA and remoteLoopB bracket the {mids} whitelist in the on-device
// BusyBox-ash streaming loop. See lp10lib/transport.remote_loop for the full
// rationale (timing-based EOF detection, adaptive idle cadence, two-step burst
// drain). It descends from the Python version's loop; the Go port additionally
// emits a one-shot @@i static device/network block (the active interface and its
// link details) before the loop and appends SoC temp, interface byte counters,
// Wi-Fi signal/link, and three ICMP ping RTTs (laptop / gateway / internet) to
// each @@s line, for the diagnostics panel.
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
const remoteLoopA = `fw=$(LUCI_local -r 5 2>/dev/null); fw=${fw#*Data:}; fw=${fw%% *}; fv=$(LUCI_local -r 6 2>/dev/null); fv=${fv#*Data:}; fv=${fv%% *}; nc=0; while read -r l; do case "$l" in processor*) nc=$((nc+1));; esac; done < /proc/cpuinfo; read -r kt < /proc/sys/kernel/ostype; read -r kr < /proc/sys/kernel/osrelease; nl=$(printf '\nx'); nl=${nl%x}; cip=${SSH_CLIENT%% *}; pg() { o=$(ping -c1 -W1 "$1" 2>/dev/null); case "$o" in *"min/avg/max = "*) o=${o#*min/avg/max = }; o=${o%% ms*}; o=${o#*/}; o=${o%%/*};; *) o=-;; esac; }; gw=; dv=; ir=$(ip route 2>/dev/null); case "$ir" in *"default via "*) r=${ir#*default via }; r=${r%%"$nl"*}; gw=${r%% *}; case "$r" in *" dev "*) dv=${r#* dev }; dv=${dv%% *};; esac;; esac; [ -z "$dv" ] && dv=eth0; mac=; read -r mac < /sys/class/net/$dv/address 2>/dev/null; ip=$(ip -o -4 addr show $dv 2>/dev/null); ip=${ip#*inet }; ip=${ip%%/*}; net=eth; sp=; dx=; ss=; fq=; rt=; if [ -d /sys/class/net/$dv/wireless ]; then net=wifi; wl=$(iw dev $dv link 2>/dev/null); case "$wl" in *"SSID: "*) ss=${wl#*SSID: }; ss=${ss%%"$nl"*};; esac; case "$wl" in *"freq: "*) fq=${wl#*freq: }; fq=${fq%%"$nl"*}; fq=${fq%% *};; esac; case "$wl" in *"tx bitrate: "*) rt=${wl#*tx bitrate: }; rt=${rt%%"$nl"*}; rt=${rt%% *};; esac; else read -r sp < /sys/class/net/$dv/speed 2>/dev/null; read -r dx < /sys/class/net/$dv/duplex 2>/dev/null; fi; bd=; ap=; pf=; while IFS= read -r ln; do case "$ln" in *build_date*\"*) bd=${ln#*\"}; bd=${bd%%\"*};; *app_svn_version*\"*) ap=${ln#*\"}; ap=${ap%%\"*};; *platform*\"*) pf=${ln#*\"}; pf=${pf%%\"*};; esac; done < /etc/fwVersion.conf 2>/dev/null; set -- $(df -k /lsync 2>/dev/null | tail -1); echo @@i; printf 'net=%s\n' "$net"; printf 'iface=%s\n' "$dv"; printf 'ip=%s\n' "$ip"; printf 'mac=%s\n' "$mac"; printf 'gw=%s\n' "$gw"; printf 'speed=%s\n' "$sp"; printf 'duplex=%s\n' "$dx"; printf 'ssid=%s\n' "$ss"; printf 'freq=%s\n' "$fq"; printf 'rate=%s\n' "$rt"; printf 'build=%s\n' "$bd"; printf 'app=%s\n' "$ap"; printf 'platform=%s\n' "$pf"; printf 'data=%s %s\n' "$3" "$2"; echo @@E; i=0; prev=; ef=0; idl=0; bw=0; dg=0; pc49=0; pgc=0; while :; do if [ $i -le 0 ]; then b=$(LUCI_local -r 42 2>/dev/null); if [ -n "$b" ] && [ "$b" != "$prev" ]; then prev=$b; echo @@B; printf '%s\n' "$b"; fi; i=5; fi; echo @@p; pn=; rd=0; pc49=$((pc49-1)); if [ $idl -lt 5 ] && [ $pc49 -le 0 ]; then pv=$(LUCI_local -r 49 2>/dev/null); echo "$pv"; pn=${pv#*Data:}; pn=${pn%% *}; rd=1; pc49=3; fi; echo @@t; tv=$(LUCI_local -r 51 2>/dev/null); echo "$tv"; echo @@v; LUCI_local -r 64 2>/dev/null; if [ "$dg" = 1 ]; then read -r la lb lc r1 r2 < /proc/loadavg; mt=0; ma=0; while read -r k v u; do case "$k" in MemTotal:) mt=$v;; MemAvailable:) ma=$v; break;; esac; done < /proc/meminfo; read -r up r3 < /proc/uptime; tp=; read -r tp < /sys/class/thermal/thermal_zone0/temp 2>/dev/null; rxb=; read -r rxb < /sys/class/net/$dv/statistics/rx_bytes 2>/dev/null; txb=; read -r txb < /sys/class/net/$dv/statistics/tx_bytes 2>/dev/null; sg=-; lq=-; if [ "$net" = wifi ]; then while read -r wf qa ql lv rest; do case "$wf" in "$dv:") lq=${ql%.}; sg=${lv%.}; break;; esac; done < /proc/net/wireless 2>/dev/null; fi; pgc=$((pgc-1)); if [ $pgc -le 0 ]; then pg "$cip"; pcl=$o; pg "$gw"; pgw=$o; pg "$ph"; pnt=$o; pgc=3; else pcl=-; pgw=-; pnt=-; fi; echo @@s; echo "$up $la $lb $lc $ma $mt $nc $fw.$fv $kt-$kr ${tp:--} ${rxb:--} ${txb:--} $sg $lq $pcl $pgw $pnt"; fi; echo @@E; i=$((i-1)); if [ $rd -eq 1 ]; then case "$pn" in ''|*[!0-9]*) lpn=;; *) [ -n "$lpn" ] && [ "$pn" -lt "$lpn" ] && { i=0; pc49=0; }; lpn=$pn;; esac; fi; [ "$tv" != "$ltv" ] && { i=0; pc49=0; }; ltv=$tv; [ $bw -gt 0 ] && { bw=$((bw-1)); i=0; }; case "$tv" in *"Data:0 "*) idl=0;; *) idl=$((idl+1));; esac; w=1; [ $idl -ge 5 ] && w=3; read -r u0 ux < /proc/uptime; if read -r -t $w mid data; then ef=0; pc=0; while :; do case "$mid" in `

const remoteLoopB = `) LUCI_local "$mid" "$data" >/dev/null 2>&1; pc=1;; 90) case "$data" in 1) dg=1;; *) dg=0;; esac;; esac; read -r -t 0 || break; read -r -t 1 mid data || break; done; [ $pc = 1 ] && { i=0; bw=4; idl=0; pc49=0; }; else read -r u1 ux < /proc/uptime; el=$(( (${u1%%.*} - ${u0%%.*}) * 100 + 1${u1#*.} - 1${u0#*.} )); if [ $el -lt 50 ]; then ef=$((ef+1)); [ $ef -ge 3 ] && exit 0; else ef=0; fi; fi; done`

// RemoteLoop returns the on-device loop script with the given command-id
// whitelist (default "40|64") and the diagnostics internet-ping target.
func RemoteLoop(mids, pingHost string) string {
	if mids == "" {
		mids = "40|64"
	}
	// ph is single-quoted; sanitizeHost guarantees no quote/metachar can escape it.
	return "ph='" + sanitizeHost(pingHost) + "'; " + remoteLoopA + mids + remoteLoopB
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

// ClassifyStderr maps residual ssh/askpass stderr to a fatal TransportError, or
// nil for transient (network) failures.
func ClassifyStderr(text string) *TransportError {
	if text == "" {
		return nil
	}
	switch {
	case strings.Contains(text, MarkerBroken):
		return &TransportError{"askpass cannot run security(1) — check PATH/sandboxing (lp10 retries every minute)", true, 60 * time.Second}
	case strings.Contains(text, MarkerLocked):
		return &TransportError{"Keychain is locked — unlock your login Keychain (lp10 retries every minute)", true, 60 * time.Second}
	case strings.Contains(text, MarkerNoItem):
		return &TransportError{"no Keychain item — run: " + KeychainHint, true, 10 * time.Second}
	case strings.Contains(text, "Permission denied"):
		return &TransportError{"SSH password rejected — update the Keychain item: " + KeychainHint, true, 10 * time.Second}
	}
	return nil
}
