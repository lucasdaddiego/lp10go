package tui

import (
	"testing"

	"github.com/lucasdaddiego/lp10/internal/mediakey"
)

func TestKeyToAction(t *testing.T) {
	cases := []struct {
		k    mediakey.Key
		want string
		ok   bool
	}{
		{mediakey.PlayPause, "toggle", true},
		{mediakey.Next, "next", true},
		{mediakey.Prev, "prev", true},
		{mediakey.KeyNone, "", false},
	}
	for _, c := range cases {
		if got, ok := keyToAction(c.k); got != c.want || ok != c.ok {
			t.Errorf("keyToAction(%v) = (%q,%v), want (%q,%v)", c.k, got, ok, c.want, c.ok)
		}
	}
}

// A mediaKeyMsg must run the mapped action through do() on the update loop — here
// the playing fixture means a next sends MID40 NEXT.
func TestMediaKeyMsgRunsAction(t *testing.T) {
	m, _, collect := makeModel(t)
	m.Update(mediaKeyMsg{action: "next"})
	if c := collect(); len(c) != 1 || c[0].Mid != 40 || c[0].Data != "NEXT" {
		t.Errorf("media next -> %+v, want [40 NEXT]", c)
	}
}
