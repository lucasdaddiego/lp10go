package protocol

import "regexp"

var reMB42 = regexp.MustCompile(`(?s)MID-Read:42 Data:(.*) Length:\d+\s*$`)

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
	mp, ok := obj.(map[string]any)
	if !ok {
		return nil, false
	}
	raw, ok := mp["Window CONTENTS"].(map[string]any)
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
