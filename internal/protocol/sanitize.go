// Coercion and sanitization at the parse boundary: everything the device sends
// is whitelist-copied into known-typed fields, mirroring lp10lib's Python
// sanitizer semantics (str()/int() coercion, isprintable stripping).

package protocol

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// Track fields the rest of the program may see, by type. Everything else from
// the device JSON is dropped at the parse boundary. Some whitelisted fields
// (Repeat/Shuffle/Seek/Skip/Next/Prev, and PlayState — see parseRecord for why
// reg 51 wins) are retained but not consumed today: they document the wire
// schema and keep the boundary stable if the UI grows into them.
var (
	trackStr  = []string{"TrackName", "Artist", "Album", "PlaybackSource", "PlayUrl", "Mime", "CoverArtUrl"}
	trackInt  = []string{"TotalTime", "Current Source", "SampleRate", "Repeat", "Shuffle", "PlayState", "ChannelCount"}
	trackBool = []string{"Seek", "Next", "Prev", "Skip"}
)

// Track is a sanitized now-playing record: string/int/bool fields only.
type Track map[string]any

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

// Int coerces a value to an int the way protocol._int does: bool -> not an int,
// int/float truncate, NaN/Inf rejected, numeric strings parsed.
func Int(v any) (int, bool) {
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
func SanitizeTrack(obj any) Track {
	m, ok := obj.(map[string]any)
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
func pyStr(v any) string {
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

// parseJSON decodes a JSON document into a generic value, returning nil on any
// error (matching json.loads -> ValueError -> None at the call sites). It uses
// UseNumber so that out-of-range literals like 1e999 parse losslessly (Python's
// json.loads accepts them as inf); the conversion to int happens later in Int,
// which then drops them, matching the Python sanitizer.
func parseJSON(s string) any {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil
	}
	return v
}
