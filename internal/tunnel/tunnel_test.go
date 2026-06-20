package tunnel

import (
	"reflect"
	"testing"
)

func TestClamp(t *testing.T) {
	cases := []struct {
		code string
		in   int
		want int
	}{
		{"MXV", 50, 50},
		{"MXV", -5, 0},
		{"MXV", 250, 100},
		{"BAS", -99, -10},
		{"BAS", 99, 10},
		{"EQS", 7, 1},
		{"VBI", 73, 73},
		{"ZZZ", 12345, 12345}, // unknown code passes through
	}
	for _, c := range cases {
		if got := Clamp(c.code, c.in); got != c.want {
			t.Errorf("Clamp(%q,%d)=%d want %d", c.code, c.in, got, c.want)
		}
	}
}

func TestSetAndQuery(t *testing.T) {
	if got := Set("MXV", 100); got != "MXV:100;" {
		t.Errorf("Set MXV 100 = %q", got)
	}
	if got := Set("MXV", 250); got != "MXV:100;" { // clamped
		t.Errorf("Set MXV 250 = %q (want clamp to 100)", got)
	}
	if got := Set("BAS", -99); got != "BAS:-10;" {
		t.Errorf("Set BAS -99 = %q (want clamp to -10)", got)
	}
	if got := Query("EQS"); got != "EQS;" {
		t.Errorf("Query EQS = %q", got)
	}
}

func TestSeedQueriesCoversEveryControl(t *testing.T) {
	if got := len(SeedQueries()); got != len(Specs) {
		t.Fatalf("SeedQueries len=%d want %d", got, len(Specs))
	}
	for i, q := range SeedQueries() {
		if q != Specs[i].Code+";" {
			t.Errorf("SeedQueries[%d]=%q want %q", i, q, Specs[i].Code+";")
		}
	}
}

func TestParseFrames(t *testing.T) {
	// the exact 7-control snapshot captured from the device
	in := "MXV:100;EQS:0;VBS:1;VBI:15;BAS:3;MID:0;TRE:3;"
	got, rest := ParseFrames(in)
	want := []Update{
		{"MXV", 100}, {"EQS", 0}, {"VBS", 1}, {"VBI", 15},
		{"BAS", 3}, {"MID", 0}, {"TRE", 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseFrames updates=%v want %v", got, want)
	}
	if rest != "" {
		t.Errorf("ParseFrames rest=%q want empty", rest)
	}
}

func TestParseFramesPartialCarry(t *testing.T) {
	got, rest := ParseFrames("MXV:100;BAS:")
	if len(got) != 1 || got[0] != (Update{"MXV", 100}) {
		t.Errorf("got=%v want one MXV:100", got)
	}
	if rest != "BAS:" {
		t.Errorf("rest=%q want %q", rest, "BAS:")
	}
	// feeding the carry + the remainder completes the frame
	got2, rest2 := ParseFrames(rest + "7;")
	if len(got2) != 1 || got2[0] != (Update{"BAS", 7}) {
		t.Errorf("got2=%v want one BAS:7", got2)
	}
	if rest2 != "" {
		t.Errorf("rest2=%q want empty", rest2)
	}
}

func TestParseFramesSkipsJunk(t *testing.T) {
	// negative tone value, a duplicated broadcast, an unknown code, a valueless
	// query echo, and a non-numeric payload
	got, _ := ParseFrames("TRE:-7;TRE:-7;XYZ:5;MXV;MXV:abc;VBS:1;")
	want := []Update{{"TRE", -7}, {"TRE", -7}, {"VBS", 1}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got=%v want %v", got, want)
	}
}
