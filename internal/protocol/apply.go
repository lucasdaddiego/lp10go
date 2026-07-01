// Applying framed records to State: the lock-free decode of each @@-section
// (parseRecord and the section parsers) and the single locked mutation
// (ApplyRecord).

package protocol

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

var reNum = regexp.MustCompile(`Data:(-?\d+)`)

// SysInfo holds the device stats from the @@s section (all kept as raw strings;
// the TUI parses them lazily for health coloring). The trailing fields are
// optional extras appended by newer device loops; "" when the loop didn't send
// them (so older loops, or the test fixtures, stay compatible).
type SysInfo struct {
	Up, Load, Avail, Total, NCPU, FW string
	OS                               string // "" when absent
	TempmC                           string // SoC temperature, milli-°C
	RxBytes, TxBytes                 string // active-iface byte counters (cumulative)
	SignalDBm, LinkQ                 string // Wi-Fi only ("" on ethernet)
	PingClient, PingGw, PingNet      string // avg RTT ms: laptop / gateway / internet ("" unmeasured)
	// Newer diag-gated extras (audio chain + contention); "" when the device loop
	// or this hardware can't provide them (e.g. /proc/asound absent, fixed-clock CPU).
	PcmState string // ALSA playback state: RUNNING / SETUP / "" (no stream)
	BufAvail string // ALSA frames free in the ring buffer (status: avail)
	DacRate  string // actual DAC clock, Hz (vs the source's claimed rate)
	DacFmt   string // actual DAC sample format, e.g. S16_LE
	DacCh    string // actual DAC channel count
	BufSize  string // ALSA ring-buffer size, frames (hw_params: buffer_size)
	CpuKHz   string // current CPU frequency, kHz
	Procs    string // /proc/loadavg running/total, e.g. "2/118"
	NoiseDBm string // Wi-Fi noise floor, dBm (SNR = SignalDBm − NoiseDBm)
	// active-iface error/drop counters (cumulative since boot; the UI shows
	// session deltas so historical noise never reads as a live fault)
	RxErrs, TxErrs string
	RxDrop, TxDrop string
}

// The @@s stats line is positional: these indices name the columns in the exact
// order the device loop emits them (the `echo "@@s ..."` in transport's
// remote_loop.src.sh — the two must change in lockstep; sysFieldCount is
// cross-checked against that emitter by TestSysStatsFieldOrder). Fields from
// sfOS on are optional extras newer loops append; "-" marks an unread value.
const (
	sfUp = iota
	sfLoad1
	sfLoad5
	sfLoad15
	sfAvail
	sfTotal
	sfNCPU
	sfFW
	sfOS
	sfTempmC
	sfRxBytes
	sfTxBytes
	sfSignalDBm
	sfLinkQ
	sfPingClient
	sfPingGw
	sfPingNet
	sfPcmState
	sfBufAvail
	sfDacRate
	sfDacFmt
	sfDacCh
	sfBufSize
	sfCpuKHz
	sfProcs
	sfNoiseDBm
	sfRxErrs
	sfTxErrs
	sfRxDrop
	sfTxDrop
	sysFieldCount
)

// sysRequired is the mandatory positional prefix (through sfFW); a shorter @@s
// line is malformed and dropped whole.
const sysRequired = sfOS

// DevInfo holds the static device/network info from the one-shot @@i section
// (key=value lines), refreshed once per connection.
type DevInfo struct {
	Net, Iface           string // "eth"|"wifi" medium, and the active interface name
	IP, MAC, Gateway     string
	Speed, Duplex        string // ethernet link: Mbit/s, "full"|"half"
	SSID, Freq, Rate     string // Wi-Fi link: network name, MHz, tx Mbit/s
	Build, App, Platform string
	Name                 string // the device's FriendlyName (reg 90); "" when unread
	DataUsed, DataTotal  string // /lsync (data partition), KB
	DNS                  string // configured resolver (first nameserver); "" when absent
}

// confKeys is the allowlist of capability ids the one-shot @@c block may carry;
// any other key is dropped at the parse boundary (mirroring DevInfo's whitelist).
// The values are "on" / "off" / "" (unknown) — see the @@c emitter in
// transport's remote_loop.sh.
var confKeys = map[string]bool{
	"spotify": true, "airplay": true, "dlna": true, "bt": true,
	"cast": true, "tidal": true, "qobuz": true, "usb": true,
}

// ConfInfo holds the device's streaming-capability state from the one-shot @@c
// section, refreshed once per connection. Svc maps a capability id (see confKeys)
// to "on" (env-enabled / daemon running), "off", or "" (unknown — the device
// couldn't read the flag). It feeds the config view; it carries no live metrics,
// so unlike @@s it is gathered unconditionally at connect, not gated on an overlay.
type ConfInfo struct {
	Svc map[string]string
}

// parsedRecord is the lock-free decode of one framed record, ready to be
// assigned under the State lock.
type parsedRecord struct {
	track Track
	idle  bool
	hasB  bool // a content-free @@B header carries no track update

	pos, play, vol       int
	posOK, playOK, volOK bool
	hadData              bool

	sysinfo  *SysInfo
	devinfo  *DevInfo
	confinfo *ConfInfo
	details  *DevDetails
	mroom    *Multiroom
}

// regInt extracts the integer register value from a section's joined lines
// (the `Data:` field of a LUCI_local read).
func regInt(rec Record, tag string) (int, bool) {
	if lines := rec[tag]; len(lines) > 0 {
		if mm := reNum.FindStringSubmatch(strings.Join(lines, "\n")); mm != nil {
			// drop an out-of-int64-range token rather than saturate to MaxInt
			if n, err := strconv.Atoi(mm[1]); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

// parseRecord decodes every section of a framed record without touching State.
//
// Play state is taken from the per-tick reg-51 read (@@t), not the MID-42
// JSON's PlayState: both encode 0 = playing, but @@t arrives every tick while
// @@B is polled every few ticks and shipped only on change, so reg 51 is always
// at least as fresh. PlayState still crosses the parse boundary inside the
// Track (see trackInt) but is deliberately not consumed.
func parseRecord(rec Record) parsedRecord {
	var p parsedRecord
	p.hasB = len(rec["B"]) > 0
	if p.hasB {
		p.track, p.idle = ParseMB42(strings.Join(rec["B"], "\n"))
	}
	p.pos, p.posOK = regInt(rec, "p")
	p.play, p.playOK = regInt(rec, "t")
	p.vol, p.volOK = regInt(rec, "v")
	p.hadData = p.hasB || p.posOK || p.playOK || p.volOK
	p.sysinfo = parseSysInfo(rec["s"])
	p.devinfo = parseDevInfo(rec["i"])
	p.confinfo = parseConfInfo(rec["c"])
	p.details = parseDevDetails(rec["d"])
	p.mroom = parseMultiroom(rec["g"])
	return p
}

// ApplyRecord applies one framed record under the State lock. last_rx stamps on
// every framed record (link liveness for the watchdog); last_data/connected
// only on records carrying data. Track updates only when a B section is present.
func ApplyRecord(st *State, rec Record) {
	p := parseRecord(rec) // lock-free: the critical section below is pure assignment
	now := time.Now()

	st.mu.Lock()
	defer st.mu.Unlock()
	st.lastRx = now
	if p.sysinfo != nil {
		st.updateNet(p.sysinfo, now)
		st.sysinfo = p.sysinfo
	}
	if p.devinfo != nil {
		st.devinfo = p.devinfo
	}
	if p.confinfo != nil {
		st.confinfo = p.confinfo
	}
	if p.details != nil {
		st.details = p.details
	}
	if p.mroom != nil {
		st.mroom = p.mroom
	}
	if p.hasB {
		switch {
		case p.track != nil:
			st.track, st.trackAt = p.track, now
			st.lastTrack = p.track
		case p.idle:
			st.track = nil // definitive idle: clear now
		case st.track == nil || now.Sub(st.trackAt) > DebounceWindow:
			st.track = nil // garbage B: debounced clear
		}
	}
	if p.posOK {
		st.posMs, st.posAt = p.pos, now
	}
	if p.playOK && !now.Before(st.playHold) {
		if p.play == 0 && st.playing != 0 && !p.posOK {
			st.posAt = now // external resume: clock restarts at last position
		}
		st.playing = p.play
	}
	if p.volOK && !now.Before(st.volHold) {
		st.vol = p.vol
	}
	if p.hadData {
		st.lastData = now
		if !st.connected {
			st.retryBase = st.attempts // badge counts per-outage
		}
		st.connected = true
		st.gotRecord = true
	}
}

// parseSysInfo parses the @@s positional stats line into a SysInfo (nil if the
// section is absent or shorter than the required prefix). See the sf* index
// constants for the column order shared with the device loop's emitter.
func parseSysInfo(lines []string) *SysInfo {
	if len(lines) == 0 {
		return nil
	}
	f := strings.Fields(printable(lines[0]))
	if len(f) < sysRequired {
		return nil
	}
	si := &SysInfo{
		Up:    f[sfUp],
		Load:  f[sfLoad1] + " " + f[sfLoad5] + " " + f[sfLoad15],
		Avail: f[sfAvail], Total: f[sfTotal], NCPU: f[sfNCPU], FW: f[sfFW],
	}
	// optional trailing extras (newer loops, diag-gated); older loops stop
	// short -> opt()="".
	opt := func(i int) string {
		if i < len(f) && f[i] != "-" {
			return f[i]
		}
		return ""
	}
	si.OS = opt(sfOS)
	si.TempmC = opt(sfTempmC)
	si.RxBytes, si.TxBytes = opt(sfRxBytes), opt(sfTxBytes)
	si.SignalDBm, si.LinkQ = opt(sfSignalDBm), opt(sfLinkQ)
	si.PingClient, si.PingGw, si.PingNet = opt(sfPingClient), opt(sfPingGw), opt(sfPingNet)
	si.PcmState, si.BufAvail = opt(sfPcmState), opt(sfBufAvail)
	si.DacRate, si.DacFmt, si.DacCh = opt(sfDacRate), opt(sfDacFmt), opt(sfDacCh)
	si.BufSize, si.CpuKHz = opt(sfBufSize), opt(sfCpuKHz)
	si.Procs, si.NoiseDBm = opt(sfProcs), opt(sfNoiseDBm)
	si.RxErrs, si.TxErrs = opt(sfRxErrs), opt(sfTxErrs)
	si.RxDrop, si.TxDrop = opt(sfRxDrop), opt(sfTxDrop)
	return si
}

// parseDevInfo parses the @@i static device/network key=value block (nil if absent).
func parseDevInfo(lines []string) *DevInfo {
	if len(lines) == 0 {
		return nil
	}
	di := &DevInfo{}
	for _, ln := range lines {
		k, v, ok := strings.Cut(printable(ln), "=")
		if !ok {
			continue
		}
		switch k {
		case "net":
			di.Net = v
		case "iface":
			di.Iface = v
		case "ip":
			di.IP = v
		case "mac":
			di.MAC = v
		case "gw":
			di.Gateway = v
		case "speed":
			di.Speed = v
		case "duplex":
			di.Duplex = v
		case "ssid":
			di.SSID = v
		case "freq":
			di.Freq = v
		case "rate":
			di.Rate = v
		case "build":
			di.Build = v
		case "app":
			di.App = v
		case "platform":
			di.Platform = v
		case "name":
			di.Name = v
		case "data":
			if ff := strings.Fields(v); len(ff) == 2 {
				di.DataUsed, di.DataTotal = ff[0], ff[1]
			}
		case "dns":
			di.DNS = v
		}
	}
	return di
}

// parseConfInfo parses the @@c capability key=value block, keeping only the
// confKeys allowlist (nil if absent).
func parseConfInfo(lines []string) *ConfInfo {
	if len(lines) == 0 {
		return nil
	}
	ci := &ConfInfo{Svc: make(map[string]string, len(confKeys))}
	for _, ln := range lines {
		if k, v, ok := strings.Cut(printable(ln), "="); ok && confKeys[k] {
			ci.Svc[k] = v
		}
	}
	return ci
}
