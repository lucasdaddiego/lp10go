// Package tunnel speaks the device's plain-text audio-control protocol: the
// LibreWireless "tcptunnelling" channel on TCP 2018, which the Arylic/Go Control
// app uses for tone, max-volume, deep-bass, and EQ. It is intentionally separate
// from the SSH player stream (transport/workers): playback rides one ssh
// connection; these device-config knobs ride this socket.
//
// Wire format: bare ASCII commands "CODE:VALUE;" (semicolon-terminated, no
// newline, no framing, no auth). Sending "CODE;" with no value is a QUERY — the
// device replies by broadcasting "CODE:VALUE;" to every connected client. A set
// from a network client reaches the MCU (verified write-through); the same
// string injected locally via `LUCI_local 112` does NOT, so this socket is the
// only way to drive these settings.
package tunnel

import (
	"strconv"
	"strings"
)

// Port is the device's control-tunnel TCP port.
const Port = 2018

// Kind distinguishes a ranged control (a slider) from a 0/1 toggle.
type Kind int

const (
	Ranged Kind = iota
	Toggle
)

// Spec describes one control: its wire code, UI label, kind, and value bounds.
// Bounds are the UI's working range; the device clamps authoritatively and
// echoes the applied value back, so a slightly-off Max here only limits the
// slider, it can't push an invalid value (the readback corrects the display).
type Spec struct {
	Code     string
	Label    string
	Kind     Kind
	Min, Max int
	Step     int
}

// Specs is the control set, in display order. Codes observed on FW AR241CE_9243:
// MXV max-volume cap, EQS named-EQ on/off, BAS/MID/TRE tone, VBS deep-bass
// switch, VBI deep-bass intensity. Tone bounds are conservative (device clamps).
var Specs = []Spec{
	{Code: "MXV", Label: "Max Volume", Kind: Ranged, Min: 0, Max: 100, Step: 5},
	{Code: "EQS", Label: "EQ", Kind: Toggle, Min: 0, Max: 1, Step: 1},
	{Code: "BAS", Label: "Bass", Kind: Ranged, Min: -10, Max: 10, Step: 1},
	{Code: "MID", Label: "Mid", Kind: Ranged, Min: -10, Max: 10, Step: 1},
	{Code: "TRE", Label: "Treble", Kind: Ranged, Min: -10, Max: 10, Step: 1},
	{Code: "VBS", Label: "Deep Bass", Kind: Toggle, Min: 0, Max: 1, Step: 1},
	{Code: "VBI", Label: "Deep Bass Lvl", Kind: Ranged, Min: 0, Max: 100, Step: 5},
}

var specByCode = func() map[string]Spec {
	m := make(map[string]Spec, len(Specs))
	for _, s := range Specs {
		m[s.Code] = s
	}
	return m
}()

// Lookup returns the Spec for a wire code (false if unknown).
func Lookup(code string) (Spec, bool) {
	s, ok := specByCode[code]
	return s, ok
}

// Clamp constrains v to a known code's [Min,Max]; an unknown code passes through.
func Clamp(code string, v int) int {
	s, ok := specByCode[code]
	if !ok {
		return v
	}
	if v < s.Min {
		return s.Min
	}
	if v > s.Max {
		return s.Max
	}
	return v
}

// Set is the wire string that assigns a value, e.g. Set("MXV", 100) == "MXV:100;".
// The value is clamped to the code's range first.
func Set(code string, v int) string {
	return code + ":" + strconv.Itoa(Clamp(code, v)) + ";"
}

// Query is the wire string that reads a value, e.g. Query("MXV") == "MXV;".
func Query(code string) string { return code + ";" }

// SeedQueries returns one query per known control, for reading current values on
// connect.
func SeedQueries() []string {
	out := make([]string, 0, len(Specs))
	for _, s := range Specs {
		out = append(out, Query(s.Code))
	}
	return out
}

// Update is one parsed "CODE:VALUE" from the device.
type Update struct {
	Code string
	Val  int
}

// ParseFrames consumes every complete ';'-terminated frame from buf and returns
// the recognized updates plus any trailing partial frame (carry it into the next
// read). Frames that are unknown codes, valueless, or non-numeric are skipped —
// a malformed burst can never panic or desync the stream.
func ParseFrames(buf string) (out []Update, rest string) {
	for {
		i := strings.IndexByte(buf, ';')
		if i < 0 {
			return out, buf // no terminator yet: keep the partial
		}
		frame := buf[:i]
		buf = buf[i+1:]
		code, val, ok := parseFrame(frame)
		if ok {
			out = append(out, Update{Code: code, Val: val})
		}
	}
}

func parseFrame(frame string) (code string, val int, ok bool) {
	c := strings.IndexByte(frame, ':')
	if c < 0 {
		return "", 0, false // bare "CODE" (our own query echo) — ignore
	}
	code = frame[:c]
	if _, known := specByCode[code]; !known {
		return "", 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(frame[c+1:]))
	if err != nil {
		return "", 0, false
	}
	return code, n, true
}
