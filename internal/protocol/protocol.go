// Package protocol implements the LUCI wire protocol: record framing, MB42
// parsing, command reduction, payload validation, and the shared State that
// worker goroutines mutate and the TUI reads. Port of lp10lib/protocol.py.
package protocol

import (
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/lucasdaddiego/lp10go/internal/config"
)

var transportWords = map[string]bool{
	"PAUSE": true, "RESUME": true, "NEXT": true, "PREV": true,
}

var (
	reMB42 = regexp.MustCompile(`(?s)MID-Read:42 Data:(.*) Length:\d+\s*$`)
	reNum  = regexp.MustCompile(`Data:(-?\d+)`)
	volRe  = regexp.MustCompile(`^\d{1,3}$`)
)

// tags is the set of section letters a record may carry. 'i' is the one-shot
// static device/network info block (key=value lines) sent once per connection.
var tags = map[byte]bool{'B': true, 'p': true, 't': true, 'v': true, 's': true, 'i': true}

const maxRecLines = 200 // a legitimate record is ~30 lines

// Echo-suppression / debounce windows.
const (
	VolHoldDuration  = 2500 * time.Millisecond
	PlayHoldDuration = 1500 * time.Millisecond
	DebounceWindow   = 3 * time.Second
	// EQHoldDuration suppresses the device's own broadcast echo of an EQ/tone
	// control just changed locally, so a rapid drag isn't fought by the echo.
	EQHoldDuration = 600 * time.Millisecond
)

// Track fields the rest of the program may see, by type. Everything else from
// the device JSON is dropped at the parse boundary.
var (
	trackStr  = []string{"TrackName", "Artist", "Album", "PlaybackSource", "PlayUrl", "Mime", "CoverArtUrl"}
	trackInt  = []string{"TotalTime", "Current Source", "SampleRate", "Repeat", "Shuffle", "PlayState", "ChannelCount"}
	trackBool = []string{"Seek", "Next", "Prev", "Skip"}
)

// Track is a sanitized now-playing record: string/int/bool fields only.
type Track map[string]interface{}

// Str returns the string field, or "" if absent or not a string.
func (t Track) Str(k string) string {
	if t == nil {
		return ""
	}
	s, _ := t[k].(string)
	return s
}

// GetInt returns the int field (0 if absent), matching `_int(t.get(k)) or 0`.
func (t Track) GetInt(k string) int {
	if t == nil {
		return 0
	}
	n, _ := Int(t[k])
	return n
}

// Record is one framed snapshot: section letter -> its lines.
type Record map[string][]string

// Proc is the live ssh child and its pipes, held by State for the writer/reaper.
// Done is closed by the single goroutine that owns Cmd.Wait(), so reaper and
// teardown can both await exit without racing on Wait (which may run only once).
type Proc struct {
	Cmd    *exec.Cmd
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	Done   chan struct{}
}

// WaitTimeout reports whether the process has exited within d.
func (p *Proc) WaitTimeout(d time.Duration) bool {
	select {
	case <-p.Done:
		return true
	case <-time.After(d):
		return false
	}
}

// Command is a queued device command carrying its own enqueue time.
type Command struct {
	Mid  int
	Data string
	TS   time.Time
}

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
}

// DevInfo holds the static device/network info from the one-shot @@i section
// (key=value lines), refreshed once per connection.
type DevInfo struct {
	Net, Iface           string // "eth"|"wifi" medium, and the active interface name
	IP, MAC, Gateway     string
	Speed, Duplex        string // ethernet link: Mbit/s, "full"|"half"
	SSID, Freq, Rate     string // Wi-Fi link: network name, MHz, tx Mbit/s
	Build, App, Platform string
	DataUsed, DataTotal  string // /lsync (data partition), KB
}

// PingStat is one latency target's rolling readout in milliseconds: the average,
// the jitter (mean absolute successive difference), the peak over the window, and
// the raw samples (oldest→newest) for a sparkline. OK is false until a sample
// arrives. Series/Peak cover only the window held while the overlay was open.
type PingStat struct {
	Avg, Jitter, Peak float64
	Series            []float64
	OK                bool
}

// NetStat is the computed network readout for the diagnostics overlay: live
// throughput (bytes/sec) over the active interface and latency to the laptop,
// the gateway, and the configured internet host.
type NetStat struct {
	RxRate, TxRate float64
	RatesOK        bool
	Ping           [3]PingStat // 0 laptop (client), 1 gateway, 2 internet
}

// IterRecords turns a line source into a sequence of framed records. The 'B'
// key is present only when an @@B section appeared. Bad lines never panic. A
// single section that grows past maxRecLines (malformed flood) is shed on its
// own — the record's other, well-formed sections are kept; framing is kept.
// nextLine returns (line, true) per line and ("", false) at EOF.
func IterRecords(nextLine func() (string, bool)) iter.Seq[Record] {
	return func(yield func(Record) bool) {
		rec := Record{}
		key := byte(0)
		n := 0 // lines accumulated into the current section (reset per section)
		for {
			line, ok := nextLine()
			if !ok {
				return // EOF: drop any partial record (no @@E)
			}
			line = strings.TrimRight(line, "\n")
			if strings.HasPrefix(line, "@@") {
				var tag byte
				if len(line) >= 3 {
					tag = line[2]
				}
				switch {
				case tag == 'E':
					if !yield(rec) {
						return
					}
					rec, key, n = Record{}, 0, 0
				case tags[tag]:
					key, n = tag, 0
					if _, exists := rec[string(key)]; !exists {
						rec[string(key)] = []string{}
					}
				default:
					key = 0
				}
			} else if key != 0 && line != "" {
				if n >= maxRecLines {
					// this section is flooding: drop what it accumulated and stop
					// appending, but keep the record's other (well-formed) sections
					delete(rec, string(key))
					key = 0
				} else {
					n++
					rec[string(key)] = append(rec[string(key)], line)
				}
			}
		}
	}
}

// Int coerces a value to an int the way protocol._int does: bool -> not an int,
// int/float truncate, NaN/Inf rejected, numeric strings parsed.
func Int(v interface{}) (int, bool) {
	switch x := v.(type) {
	case bool:
		return 0, false
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, false
		}
		return int(x), true
	case float32:
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return int(f), true
	case json.Number: // from UseNumber decoding (int or float literal)
		// Try an integer parse first so large integers keep full int64
		// precision (Python's int is arbitrary precision); fall back to float
		// for non-integer literals, dropping NaN/Inf (e.g. 1e999).
		if i, err := strconv.ParseInt(string(x), 10, 64); err == nil {
			return int(i), true
		}
		f, err := strconv.ParseFloat(string(x), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return int(f), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// printable strips control/separator characters the way CPython's
// str.isprintable does: non-printable == category Other (C*) or Separator (Z*),
// except the ASCII space. Using the category test (rather than Go's
// unicode.IsPrint) keeps characters that are assigned in a newer Unicode version
// than Go's tables, matching Python more closely.
func printable(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c == ' ' || !unicode.In(c, unicode.C, unicode.Z) {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// SanitizeTrack whitelist-copies device/snapshot track JSON into known-typed
// fields. Returns a (possibly empty) Track, or nil when obj is not an object.
func SanitizeTrack(obj interface{}) Track {
	m, ok := obj.(map[string]interface{})
	if !ok {
		return nil
	}
	t := Track{}
	for _, k := range trackStr {
		v, present := m[k]
		if !present || v == nil {
			continue
		}
		s, isStr := v.(string)
		if !isStr {
			s = pyStr(v)
		}
		if s = printable(s); s != "" {
			t[k] = s
		}
	}
	for _, k := range trackInt {
		if n, ok := Int(m[k]); ok {
			t[k] = n
		}
	}
	for _, k := range trackBool {
		if b, ok := m[k].(bool); ok {
			t[k] = b
		}
	}
	return t
}

// pyStr mirrors Python's str() for the non-string values that may land in a
// string field (Python str(True) == "True", str(1.5) == "1.5").
func pyStr(v interface{}) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case json.Number:
		return x.String()
	case string:
		return x
	default:
		return fmt.Sprintf("%v", x)
	}
}

// ParseMB42 turns a joined B-section into a sanitized Track, or signals idle.
// Returns (track, false) for a real track, (nil, true) for a definitive idle
// PlayView (clear the track now), and (nil, false) for unparseable garbage
// (debounce the clear).
func ParseMB42(block string) (Track, bool) {
	if block == "" {
		return nil, false
	}
	m := reMB42.FindStringSubmatch(block)
	if m == nil {
		return nil, false
	}
	obj := parseJSON(m[1])
	mp, ok := obj.(map[string]interface{})
	if !ok {
		return nil, false
	}
	raw, ok := mp["Window CONTENTS"].(map[string]interface{})
	if !ok {
		return nil, false
	}
	t := SanitizeTrack(raw)
	name := t.Str("TrackName")
	total := t.GetInt("TotalTime")
	src := t.GetInt("Current Source")
	if name == "" && total <= 0 && src == 0 {
		return nil, true // definitive idle
	}
	return t, false
}

// ApplyRecord applies one framed record under the State lock. last_rx stamps on
// every framed record (link liveness for the watchdog); last_data/connected
// only on records carrying data. Track updates only when a B section is present.
func ApplyRecord(st *State, rec Record) {
	now := time.Now()

	var track Track
	var idle bool
	hasB := len(rec["B"]) > 0 // a content-free @@B header carries no track update
	if hasB {
		track, idle = ParseMB42(strings.Join(rec["B"], "\n"))
	}

	num := func(tag string) (int, bool) {
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

	pos, posOK := num("p")
	play, playOK := num("t")
	vol, volOK := num("v")
	hadData := len(rec["B"]) > 0 || posOK || playOK || volOK

	var sysinfo *SysInfo
	if lines := rec["s"]; len(lines) > 0 {
		f := strings.Fields(printable(lines[0]))
		if len(f) >= 8 {
			si := &SysInfo{
				Up:    f[0],
				Load:  f[1] + " " + f[2] + " " + f[3],
				Avail: f[4], Total: f[5], NCPU: f[6], FW: f[7],
			}
			// optional trailing extras (newer loops, diag-gated): OS, SoC temp,
			// rx/tx byte counters, Wi-Fi signal/link, then the three ping RTTs.
			// "-" is the loop's placeholder for a value it couldn't read.
			opt := func(i int) string {
				if i < len(f) && f[i] != "-" {
					return f[i]
				}
				return ""
			}
			si.OS = opt(8)
			si.TempmC = opt(9)
			si.RxBytes, si.TxBytes = opt(10), opt(11)
			si.SignalDBm, si.LinkQ = opt(12), opt(13)
			si.PingClient, si.PingGw, si.PingNet = opt(14), opt(15), opt(16)
			sysinfo = si
		}
	}

	var devinfo *DevInfo
	if lines := rec["i"]; len(lines) > 0 {
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
			case "data":
				if ff := strings.Fields(v); len(ff) == 2 {
					di.DataUsed, di.DataTotal = ff[0], ff[1]
				}
			}
		}
		devinfo = di
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	st.lastRx = now
	if sysinfo != nil {
		st.updateNet(sysinfo, now)
		st.sysinfo = sysinfo
	}
	if devinfo != nil {
		st.devinfo = devinfo
	}
	if hasB {
		switch {
		case track != nil:
			st.track, st.trackAt = track, now
			st.lastTrack = track
		case idle:
			st.track = nil // definitive idle: clear now
		case st.track == nil || now.Sub(st.trackAt) > DebounceWindow:
			st.track = nil // garbage B: debounced clear
		}
	}
	if posOK {
		st.posMs, st.posAt = pos, now
	}
	if playOK && !now.Before(st.playHold) {
		if play == 0 && st.playing != 0 && !posOK {
			st.posAt = now // external resume: clock restarts at last position
		}
		st.playing = play
	}
	if volOK && !now.Before(st.volHold) {
		st.vol = vol
	}
	if hadData {
		st.lastData = now
		if !st.connected {
			st.retryBase = st.attempts // badge counts per-outage
		}
		st.connected = true
		st.gotRecord = true
	}
}

// ReduceCommands collapses a command list: last volume wins (at its own
// position), consecutive PAUSE/RESUME runs collapse to the final one, every
// NEXT/PREV is preserved, order stable.
func ReduceCommands(cmds []Command) []Command {
	out := make([]Command, 0, len(cmds))
	for _, c := range cmds {
		switch {
		case c.Mid == 64 || c.Mid == 90:
			// last value wins for volume (64) and the stats toggle (90):
			// drop any earlier command with the same mid, keep this one
			kept := out[:0]
			for _, cc := range out {
				if cc.Mid != c.Mid {
					kept = append(kept, cc)
				}
			}
			out = append(kept, c)
		case c.Mid == 40 && (c.Data == "PAUSE" || c.Data == "RESUME") &&
			len(out) > 0 && out[len(out)-1].Mid == 40 &&
			(out[len(out)-1].Data == "PAUSE" || out[len(out)-1].Data == "RESUME"):
			out[len(out)-1] = c
		default:
			out = append(out, c)
		}
	}
	return out
}

// ValidatePayload whitelists what may be written to the device's stdin. MID 90
// is the diagnostics-stats toggle (1 = overlay open, send @@s; 0 = closed): it
// only ever flips a flag on the device, never reaches LUCI_local.
func ValidatePayload(mid int, data string) bool {
	switch mid {
	case 40:
		return transportWords[data]
	case 64:
		if !volRe.MatchString(data) {
			return false
		}
		n, _ := strconv.Atoi(data)
		return n <= 100
	case 90:
		return data == "0" || data == "1"
	}
	return false
}

// Event is a one-shot, broadcast signal mirroring threading.Event: Set is
// idempotent, IsSet is a non-blocking check, Wait blocks up to d and reports
// whether the event was set.
type Event struct {
	mu  sync.Mutex
	ch  chan struct{}
	set bool
}

func NewEvent() *Event { return &Event{ch: make(chan struct{})} }

func (e *Event) Set() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.set {
		e.set = true
		close(e.ch)
	}
}

func (e *Event) IsSet() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.set
}

// Wait blocks until the event is set or d elapses; returns true iff set.
func (e *Event) Wait(d time.Duration) bool {
	if e.IsSet() {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-e.ch:
		return true
	case <-t.C:
		return e.IsSet()
	}
}

// State is the shared, lock-protected model the workers mutate and the TUI
// reads. Field semantics match lp10lib.protocol.State.
type State struct {
	mu    sync.Mutex
	WLock sync.Mutex // serializes sproc stdin write vs close

	Stop    *Event
	Drained *Event // command_worker flushed for quit

	connected bool
	track     Track
	trackAt   time.Time
	lastTrack Track // survives idle, feeds the idle screen
	sysinfo   *SysInfo
	devinfo   *DevInfo // static device/network info (@@i, once per connection)

	posMs    int
	posAt    time.Time
	playing  int // MID 51: 0=playing, anything else not
	vol      int
	volHold  time.Time
	playHold time.Time
	premute  int // 0 == none (Python None)

	PremuteFile  string
	SnapshotFile string

	errMsg string
	errAt  time.Time
	fatal  bool

	sproc     *Proc
	spawnedAt time.Time
	gotRecord bool
	lastRx    time.Time // zero == never (this connection)
	lastData  time.Time // zero == never (this connection)
	attempts  int
	retryBase int // attempts at last successful connect

	// network throughput + latency for the diagnostics overlay, over the active
	// interface. Rates derive from the cumulative byte counters against the prior
	// @@s; the ping rings hold recent RTTs (ms) for laptop/gateway/internet.
	netPrevRx, netPrevTx int64
	netPrevAt            time.Time
	netRxRate, netTxRate float64
	netRatesOK           bool
	pingRing             [3][]float64

	// EQ / tone control state from the :2018 tunnel (separate from the ssh
	// player stream). Keyed by wire code (MXV/EQS/BAS/MID/TRE/VBS/VBI).
	eqConnected bool
	eqVals      map[string]int       // wire code -> last-known value
	eqHold      map[string]time.Time // wire code -> echo-suppression deadline
}

// NewState returns an initialized State, mirroring the Python constructor
// defaults (playing starts at 2 = "not playing", posAt = now).
func NewState() *State {
	return &State{
		Stop:    NewEvent(),
		Drained: NewEvent(),
		playing: 2,
		posAt:   time.Now(),
		eqVals:  map[string]int{},
		eqHold:  map[string]time.Time{},
	}
}

// Snapshot is an immutable view of State for rendering.
type Snapshot struct {
	Connected bool
	Track     Track
	Pos       int
	Playing   int
	Vol       int
	Muted     bool
	Error     string
	ErrorAt   time.Time
	Fatal     bool
	Attempts  int
}

// Snap projects the current State, advancing the position clock while playing.
func (st *State) Snap() Snapshot {
	st.mu.Lock()
	defer st.mu.Unlock()
	pos := st.posMs
	t := st.track
	if st.playing == 0 && t != nil && st.connected {
		pos += int(time.Since(st.posAt).Milliseconds())
	}
	if total := t.GetInt("TotalTime"); total > 0 && pos > total {
		pos = total
	}
	return Snapshot{
		Connected: st.connected,
		Track:     t,
		Pos:       pos,
		Playing:   st.playing,
		Vol:       st.vol,
		Muted:     st.connected && st.vol == 0,
		Error:     st.errMsg,
		ErrorAt:   st.errAt,
		Fatal:     st.fatal,
		Attempts:  st.attempts - st.retryBase,
	}
}

func clamp100(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// setVolLocked sets the volume and arms the echo-suppression hold, capturing a
// pre-mute level to persist (returned) when transitioning into mute.
func (st *State) setVolLocked(v int) (int, int) {
	v = clamp100(v)
	persist := 0
	if st.vol > 0 && v == 0 {
		st.premute = st.vol
		persist = st.vol
	}
	if v > 0 {
		st.premute = 0
	}
	st.vol = v
	st.volHold = time.Now().Add(VolHoldDuration)
	return v, persist
}

// SetVol sets an absolute volume, persisting a pre-mute level if muting.
func (st *State) SetVol(v int) int {
	st.mu.Lock()
	v, persist := st.setVolLocked(v)
	path := st.PremuteFile
	st.mu.Unlock()
	if persist > 0 && path != "" {
		config.SavePremute(path, persist)
	}
	return v
}

// AdjustVol changes the volume by delta, persisting a pre-mute level if muting.
func (st *State) AdjustVol(delta int) int {
	st.mu.Lock()
	v, persist := st.setVolLocked(st.vol + delta)
	path := st.PremuteFile
	st.mu.Unlock()
	if persist > 0 && path != "" {
		config.SavePremute(path, persist)
	}
	return v
}

// VolAndPremute reads the current volume and pre-mute level atomically (used by
// the mute toggle).
func (st *State) VolAndPremute() (int, int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.vol, st.premute
}

// ---- EQ / tone control state (the :2018 tunnel) ----

// ApplyTunnel records a device-reported control value, unless that control was
// changed locally within its echo-suppression window. Marks the tunnel live.
func (st *State) ApplyTunnel(code string, val int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.eqConnected = true
	if h, ok := st.eqHold[code]; ok && time.Now().Before(h) {
		return
	}
	st.eqVals[code] = val
}

// SetEQConnected sets the tunnel link state (false on disconnect/reconnect).
func (st *State) SetEQConnected(b bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.eqConnected = b
}

// SetEQLocal optimistically records a user change and arms the echo hold so the
// device's broadcast echo doesn't fight a rapid adjustment.
func (st *State) SetEQLocal(code string, val int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.eqVals[code] = val
	st.eqHold[code] = time.Now().Add(EQHoldDuration)
}

// PreloadEQ seeds cached EQ/tone values for an instant first paint of the
// equalizer, before the :2018 tunnel has connected. It does NOT arm the echo
// hold or mark the tunnel connected, so the device's authoritative seed values
// overwrite these the moment the tunnel comes up (mirroring Preload for the
// player snapshot).
func (st *State) PreloadEQ(vals map[string]int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for code, v := range vals {
		st.eqVals[code] = v
	}
}

// EQValue returns one control's last-known value and whether it is known yet.
func (st *State) EQValue(code string) (int, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	v, ok := st.eqVals[code]
	return v, ok
}

// EQView snapshots the tunnel link state and a copy of all known control values
// for rendering, in one locked read.
func (st *State) EQView() (connected bool, vals map[string]int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	vals = make(map[string]int, len(st.eqVals))
	for k, v := range st.eqVals {
		vals[k] = v
	}
	return st.eqConnected, vals
}

// Note records a transient error message (no-op once fatal).
func (st *State) Note(msg string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.fatal {
		st.errMsg, st.errAt = msg, time.Now()
	}
}

// ---- accessors used by the workers (separate package) and the TUI ----

// StartProc records a freshly spawned ssh child: liveness is per-connection, so
// last_rx/last_data reset and a fresh spawn must prove itself again.
func (st *State) StartProc(p *Proc) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.sproc = p
	st.spawnedAt = time.Now()
	st.gotRecord = false
	st.lastRx = time.Time{}
	st.lastData = time.Time{}
	st.netPrevAt = time.Time{} // re-baseline throughput; latency rings start fresh
	st.netRatesOK = false
	st.pingRing = [3][]float64{}
	st.attempts++
}

// ClearFatalOnData clears a fatal error once data flows again (self-healing).
func (st *State) ClearFatalOnData() {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.fatal {
		st.fatal = false
		st.errMsg = ""
	}
}

// SetFatal latches a fatal error with its timestamp.
func (st *State) SetFatal(msg string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.errMsg, st.errAt, st.fatal = msg, time.Now(), true
}

// Reap marks the connection dead and drops the proc handle (idempotent).
func (st *State) Reap() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.connected = false
	st.sproc = nil
}

// Sproc returns the current ssh child handle (or nil).
func (st *State) Sproc() *Proc {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.sproc
}

// WatchdogView snapshots the fields the watchdog needs in one locked read.
func (st *State) WatchdogView() (proc *Proc, spawned, lastRx, lastData time.Time, got bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.sproc, st.spawnedAt, st.lastRx, st.lastData, st.gotRecord
}

// WriterTarget returns the proc to write to and whether the session is live
// enough to accept a write: a young connection still handshaking is writable
// (ssh buffers stdin); a session that went data-silent is a wedge to hold for.
func (st *State) WriterTarget(now time.Time, liveTimeout time.Duration) (*Proc, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	live := (!st.lastData.IsZero() && now.Sub(st.lastData) <= liveTimeout) ||
		now.Sub(st.spawnedAt) <= liveTimeout
	return st.sproc, live
}

// LastTrackAndRx returns the last-seen track (survives idle) and last-rx time.
func (st *State) LastTrackAndRx() (Track, time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.lastTrack, st.lastRx
}

// DiagView snapshots the fields the diagnostics overlay reads in one lock.
func (st *State) DiagView() (lastRx, lastData time.Time, attempts int, errMsg string, sysinfo *SysInfo) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.lastRx, st.lastData, st.attempts, st.errMsg, st.sysinfo
}

// DevInfoView returns the static device/network info (or nil before the first
// @@i block arrives).
func (st *State) DevInfoView() *DevInfo {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.devinfo
}

// pingRingMax bounds each latency ring; at ~1 sample/sec while the overlay is
// open it is a ~30s window for the rolling average, jitter, peak, and sparkline.
const pingRingMax = 30

// updateNet folds one @@s sample into the throughput rates and latency rings.
// Caller holds st.mu. Rates need a prior sample with elapsed time and monotonic
// counters (an interface flap or reboot zeroes them — skip rather than spike).
func (st *State) updateNet(si *SysInfo, now time.Time) {
	rx, rxErr := strconv.ParseInt(si.RxBytes, 10, 64)
	tx, txErr := strconv.ParseInt(si.TxBytes, 10, 64)
	if rxErr == nil && txErr == nil {
		if !st.netPrevAt.IsZero() {
			if dt := now.Sub(st.netPrevAt).Seconds(); dt > 0 && rx >= st.netPrevRx && tx >= st.netPrevTx {
				st.netRxRate = float64(rx-st.netPrevRx) / dt
				st.netTxRate = float64(tx-st.netPrevTx) / dt
				st.netRatesOK = true
			}
		}
		st.netPrevRx, st.netPrevTx, st.netPrevAt = rx, tx, now
	}
	for i, s := range [3]string{si.PingClient, si.PingGw, si.PingNet} {
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			st.pingRing[i] = append(st.pingRing[i], v)
			if len(st.pingRing[i]) > pingRingMax {
				st.pingRing[i] = st.pingRing[i][len(st.pingRing[i])-pingRingMax:]
			}
		}
	}
}

// NetView returns the computed throughput + latency readout for the overlay.
func (st *State) NetView() NetStat {
	st.mu.Lock()
	defer st.mu.Unlock()
	ns := NetStat{RxRate: st.netRxRate, TxRate: st.netTxRate, RatesOK: st.netRatesOK}
	for i := range st.pingRing {
		ns.Ping[i] = pingStat(st.pingRing[i])
	}
	return ns
}

// pingStat reduces a latency ring to an average and a jitter (the mean absolute
// difference between successive samples — RFC 3550-style packet delay variation).
func pingStat(r []float64) PingStat {
	if len(r) == 0 {
		return PingStat{}
	}
	ps := PingStat{OK: true, Peak: r[0], Series: append([]float64(nil), r...)}
	var sum float64
	for _, v := range r {
		sum += v
		if v > ps.Peak {
			ps.Peak = v
		}
	}
	ps.Avg = sum / float64(len(r))
	if len(r) > 1 {
		var ds float64
		for i := 1; i < len(r); i++ {
			d := r[i] - r[i-1]
			if d < 0 {
				d = -d
			}
			ds += d
		}
		ps.Jitter = ds / float64(len(r)-1)
	}
	return ps
}

// Preload seeds the cached track/pos/vol for an instant first paint. The clock
// never resumes from a cached position, so playing starts at 2 (not playing)
// and trackAt is the zero time (any garbage B immediately clears a stale track).
func (st *State) Preload(track Track, pos, vol int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.track = track
	st.lastTrack = track
	st.trackAt = time.Time{}
	st.posMs = pos
	st.playing = 2
	st.vol = vol
}

// RawPos returns the un-extrapolated position (the last position the device
// reported), used by tests to distinguish a parsed update from clock drift.
func (st *State) RawPos() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.posMs
}

// RawAttempts returns the total connection-attempt counter.
func (st *State) RawAttempts() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.attempts
}

// ToggleOptimistic flips the local play state, arms the echo-suppression hold,
// and restarts the position clock; it returns whether the player WAS playing
// (so the caller sends PAUSE vs RESUME).
func (st *State) ToggleOptimistic() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	playing := st.playing == 0
	if playing {
		st.playing = 2
	} else {
		st.playing = 0
	}
	now := time.Now()
	st.playHold = now.Add(PlayHoldDuration)
	st.posAt = now
	return playing
}

// parseJSON decodes a JSON document into a generic value, returning nil on any
// error (matching json.loads -> ValueError -> None at the call sites). It uses
// UseNumber so that out-of-range literals like 1e999 parse losslessly (Python's
// json.loads accepts them as inf); the conversion to int happens later in Int,
// which then drops them, matching the Python sanitizer.
func parseJSON(s string) interface{} {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v interface{}
	if dec.Decode(&v) != nil {
		return nil
	}
	return v
}
