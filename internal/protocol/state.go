// The shared State: the lock-protected model the worker goroutines mutate and
// the TUI reads, its immutable Snapshot projection, and the accessor methods
// grouped by concern (volume/mute, EQ tunnel, proc/liveness, diag views).

package protocol

import (
	"image"
	"image/color"
	"io"
	"maps"
	"os/exec"
	"sync"
	"time"

	"github.com/lucasdaddiego/lp10/internal/config"
)

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
	devinfo   *DevInfo    // static device/network info (@@i, once per connection)
	confinfo  *ConfInfo   // streaming-capability state (@@c, once per connection)
	details   *DevDetails // device-details JSON readout (@@d, once per connection)
	mroom     *Multiroom  // multiroom-group readout (@@g, once per connection)

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

	// cumulative interface error/drop counters: the connection's first sample
	// baselines the session, so boot-lifetime noise (e.g. a powerline link's
	// historical drops) never reads as a live fault.
	errBase, errCur [4]int64 // rx_errors, tx_errors, rx_dropped, tx_dropped
	errsOK          bool

	// EQ / tone control state from the :2018 tunnel (separate from the ssh
	// player stream). Keyed by wire code (MXV/EQS/BAS/MID/TRE/VBS/VBI).
	eqConnected bool
	eqVals      map[string]int       // wire code -> last-known value
	eqHold      map[string]time.Time // wire code -> echo-suppression deadline

	// album art: the decoded cover and the CoverArtUrl it was loaded for, set
	// by the art worker. Snap exposes the image only while artURL still matches
	// the playing track, so a stale cover never lingers across a track change.
	artURL   string
	artImg   image.Image
	artDom   color.RGBA // cover's representative hue (computed by the art worker)
	artDomOK bool       // false for a greyscale cover (keep the theme default)
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

	CoverURL string      // current track's cover art URL ("" if none)
	Art      image.Image // decoded cover for CoverURL, or nil if not yet loaded
	// Dominant is the cover's representative hue, precomputed by the art worker so
	// the renderer never scans pixels; DominantOK is false for a greyscale cover or
	// before the cover loads. Valid only while Art is non-nil.
	Dominant   color.RGBA
	DominantOK bool

	// LastArt is the most-recently-decoded cover and the URL it came from,
	// retained across idle so the idle screen can show a dimmed "ghost" of the
	// last thing played. Unlike Art, it is not gated on the current track.
	LastArt      image.Image
	LastCoverURL string
}

// SetArt stores the decoded cover image for url (a track's CoverArtUrl) plus its
// precomputed dominant hue (dom/domOK), computed by the art worker off the render
// path. The art worker calls this; Snap only surfaces them while url is still the
// playing track's cover.
func (st *State) SetArt(url string, img image.Image, dom color.RGBA, domOK bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.artURL = url
	st.artImg = img
	st.artDom, st.artDomOK = dom, domOK
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
	cover := t.Str("CoverArtUrl")
	var art image.Image
	var dom color.RGBA
	var domOK bool
	if cover != "" && cover == st.artURL {
		art = st.artImg
		dom, domOK = st.artDom, st.artDomOK
	}
	return Snapshot{
		Connected:    st.connected,
		Track:        t,
		Pos:          pos,
		Playing:      st.playing,
		Vol:          st.vol,
		Muted:        st.connected && st.vol == 0,
		Error:        st.errMsg,
		ErrorAt:      st.errAt,
		Fatal:        st.fatal,
		Attempts:     st.attempts - st.retryBase,
		CoverURL:     cover,
		Art:          art,
		Dominant:     dom,
		DominantOK:   domOK,
		LastArt:      st.artImg,
		LastCoverURL: st.artURL,
	}
}

// ---- volume / mute ----

func clamp100(v int) int { return max(0, min(100, v)) }

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

// applyVol computes the target from the current volume under the lock, applies
// it, and persists a captured pre-mute level outside the lock.
func (st *State) applyVol(target func(cur int) int) int {
	st.mu.Lock()
	v, persist := st.setVolLocked(target(st.vol))
	path := st.PremuteFile
	st.mu.Unlock()
	if persist > 0 && path != "" {
		config.SavePremute(path, persist)
	}
	return v
}

// SetVol sets an absolute volume, persisting a pre-mute level if muting.
func (st *State) SetVol(v int) int {
	return st.applyVol(func(int) int { return v })
}

// AdjustVol changes the volume by delta, persisting a pre-mute level if muting.
func (st *State) AdjustVol(delta int) int {
	return st.applyVol(func(cur int) int { return cur + delta })
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
	maps.Copy(st.eqVals, vals)
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
	return st.eqConnected, maps.Clone(st.eqVals)
}

// ---- errors ----

// Note records a transient error message (no-op once fatal).
func (st *State) Note(msg string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.fatal {
		st.errMsg, st.errAt = msg, time.Now()
	}
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

// ---- proc / liveness (used by the workers and the TUI) ----

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
	st.errsOK = false // error counters re-baseline on the next sample
	st.attempts++
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

// ---- diagnostics views ----

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

// ConfView returns the streaming-capability state (or nil before the first @@c
// block arrives). The returned ConfInfo is owned by the caller's read: the worker
// only ever replaces st.confinfo wholesale (never mutates a published map), so the
// map is safe to range without copying.
func (st *State) ConfView() *ConfInfo {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.confinfo
}

// DetailsView returns the device-details readout (or nil before the first @@d
// block arrives). Replaced wholesale like the other one-shot blocks.
func (st *State) DetailsView() *DevDetails {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.details
}

// MultiroomView returns the multiroom-group readout (or nil before the first
// @@g block arrives).
func (st *State) MultiroomView() *Multiroom {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.mroom
}

// ---- preload / optimistic UI ----

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
