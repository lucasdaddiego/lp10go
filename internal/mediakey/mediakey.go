// Package mediakey captures the macOS keyboard's media transport keys
// (play/pause, next, previous) system-wide, so lp10 can drive the device even
// when its terminal window doesn't have focus. It is a no-op on non-macOS
// platforms.
//
// On macOS it installs a CoreGraphics session event tap (see mediakey_darwin.m)
// on its own locked OS thread with a CFRunLoop. Captured keys are classified
// here (classify) and, while the caller reports the device connected, consumed
// so other media apps don't also react (decide); when disconnected they pass
// through untouched. Installing the tap needs Accessibility permission granted to
// the host terminal app.
package mediakey

// Key is a media transport key forwarded to the caller.
type Key int

const (
	KeyNone   Key = iota // not a transport key we handle
	PlayPause            // NX_KEYTYPE_PLAY
	Next                 // NX_KEYTYPE_NEXT / NX_KEYTYPE_FAST
	Prev                 // NX_KEYTYPE_PREVIOUS / NX_KEYTYPE_REWIND
)

// Config wires the tap to the application.
type Config struct {
	// OnKey is called (on the tap's thread) for each handled key-down while
	// Connected reports true. It must be safe to call from another goroutine and
	// must not block — hand the event off (e.g. tea.Program.Send) and return.
	OnKey func(Key)
	// Connected reports whether lp10 is currently connected to the device. When
	// false the tap passes media keys through to other apps untouched; when true
	// it consumes them. Called on the tap's thread for every media key.
	Connected func() bool
	// OnActive, if set, is called (on the tap's thread) when the tap becomes
	// active after an earlier failed attempt — i.e. Accessibility was granted
	// while lp10 was already running and the retry loop re-armed. It is NOT called
	// on a clean first install, so the normal path stays silent.
	OnActive func()
}

// NX_KEYTYPE_* aux-key codes carried in a macOS system-defined event's data1.
const (
	nxKeyPlay   = 16
	nxKeyNext   = 17
	nxKeyPrev   = 18
	nxKeyFast   = 19
	nxKeyRewind = 20
)

// nxKeyDown is the data1 key-state nibble for a press (0x0B is a release).
const nxKeyDown = 0x0A

// classify maps an NX aux-key code to the Key we forward (KeyNone otherwise).
func classify(keyCode int) Key {
	switch keyCode {
	case nxKeyPlay:
		return PlayPause
	case nxKeyNext, nxKeyFast:
		return Next
	case nxKeyPrev, nxKeyRewind:
		return Prev
	}
	return KeyNone
}

// decide reports whether to fire the action (act) and whether to consume the
// event so other apps never see it (swallow), given the classified key, whether
// this is the key-down edge, and whether the device is connected. While
// connected we swallow both edges of a handled key (so no app sees a half event)
// but act only on the down edge; an unhandled key or a disconnected device
// passes through untouched.
func decide(k Key, down, connected bool) (act, swallow bool) {
	if k == KeyNone || !connected {
		return false, false
	}
	return down, true
}
