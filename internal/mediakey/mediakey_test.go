package mediakey

import "testing"

func TestClassify(t *testing.T) {
	cases := map[int]Key{
		nxKeyPlay:   PlayPause,
		nxKeyNext:   Next,
		nxKeyFast:   Next,
		nxKeyPrev:   Prev,
		nxKeyRewind: Prev,
		0:           KeyNone, // NX_KEYTYPE_SOUND_UP — volume, out of scope
		7:           KeyNone, // NX_KEYTYPE_MUTE
		99:          KeyNone,
	}
	for code, want := range cases {
		if got := classify(code); got != want {
			t.Errorf("classify(%d) = %v, want %v", code, got, want)
		}
	}
}

func TestDecide(t *testing.T) {
	// Unhandled key: never act, never swallow, regardless of connection.
	if act, sw := decide(KeyNone, true, true); act || sw {
		t.Errorf("KeyNone -> (%v,%v), want (false,false)", act, sw)
	}
	// Disconnected: pass a handled key through untouched.
	if act, sw := decide(PlayPause, true, false); act || sw {
		t.Errorf("disconnected -> (%v,%v), want (false,false)", act, sw)
	}
	// Connected key-down: act and swallow.
	if act, sw := decide(Next, true, true); !act || !sw {
		t.Errorf("down connected -> (%v,%v), want (true,true)", act, sw)
	}
	// Connected key-up: swallow (hide the paired edge) but don't act.
	if act, sw := decide(Next, false, true); act || !sw {
		t.Errorf("up connected -> (%v,%v), want (false,true)", act, sw)
	}
}
