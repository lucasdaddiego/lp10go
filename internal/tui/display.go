// Package tui is the Bubble Tea terminal UI: rendering, input dispatch, and the
// display-formatting helpers. Port of lp10lib/tui.py.
package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/lucasdaddiego/lp10go/internal/protocol"
	"golang.org/x/text/width"
)

// amb is the rendered width of ambiguous-width East Asian glyphs: 2 on
// CJK-configured terminals (where they render double-width), 1 otherwise. Glyph
// choice follows the same locale.
var amb = detectAmb()

// GL is the glyph set, with ASCII fallbacks when amb == 2 (so every positioned
// glyph is width-1).
var GL = glyphs(amb)

func detectAmb() int {
	loc := os.Getenv("LC_ALL")
	if loc == "" {
		loc = os.Getenv("LANG")
	}
	if len(loc) >= 2 {
		switch strings.ToLower(loc[:2]) {
		case "ja", "ko", "zh":
			return 2
		}
	}
	return 1
}

func glyphs(amb int) map[string]string {
	if amb == 2 {
		return map[string]string{
			"play": ">", "pause": "#", "rew": "<<", "ff": ">>", "note": "*", "warn": "!",
			"fill": "=", "half": "=", "track": "-",
			"gl": "[", "gr": "]", "gfull": "#", "gempty": ".",
			"tl": "+", "tr": "+", "bl": "+", "br": "+", "h": "-", "v": "|",
			"rep": "R", "shuf": "S", "cursor": "|", "ell": "...",
		}
	}
	return map[string]string{
		"play": "▶", "pause": "⏸", "rew": "◀◀", "ff": "▶▶", "note": "♪", "warn": "⚠",
		"fill": "━", "half": "╸", "track": "─",
		"gl": "▕", "gr": "▏", "gfull": "█", "gempty": "░",
		"tl": "╭", "tr": "╮", "bl": "╰", "br": "╯", "h": "─", "v": "│",
		"rep": "⟳", "shuf": "⇄", "cursor": "▏", "ell": "…",
	}
}

// FmtMs formats milliseconds as MM:SS.
func FmtMs(ms int) string {
	if ms < 0 {
		ms = 0
	}
	s := ms / 1000
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

// charW is the rendered width of one rune, mirroring unicodedata.east_asian_width:
// W/F -> 2, A -> amb, everything else -> 1.
func charW(r rune) int {
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	case width.EastAsianAmbiguous:
		return amb
	default:
		return 1
	}
}

// DispW is the total rendered width of a string.
func DispW(s string) int {
	w := 0
	for _, r := range s {
		w += charW(r)
	}
	return w
}

// Clip truncates s to display width w, appending the ellipsis glyph when it
// overflows. Returns "" for non-positive widths.
func Clip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if DispW(s) <= w {
		return s
	}
	ell := GL["ell"]
	ew := DispW(ell)
	if w <= ew {
		// no room for the ellipsis itself (e.g. the width-3 ASCII "..." on a
		// CJK terminal at w<3): hard-truncate to width w, no ellipsis.
		var b strings.Builder
		used := 0
		for _, ch := range s {
			cw := charW(ch)
			if used+cw > w {
				break
			}
			b.WriteRune(ch)
			used += cw
		}
		return b.String()
	}
	var b strings.Builder
	used := 0
	for _, ch := range s {
		cw := charW(ch)
		if used+cw > w-ew {
			break
		}
		b.WriteRune(ch)
		used += cw
	}
	return b.String() + ell
}

// Wrap greedily word-wraps s to at most maxLines lines of display width w. The
// last line is ellipsized only if text still overflows; a lone word wider than
// w is clipped. Returns nil for blank input.
func Wrap(s string, w, maxLines int) []string {
	if w <= 0 || s == "" {
		return nil
	}
	if DispW(s) <= w {
		return []string{s}
	}
	words := strings.Fields(s)
	var lines []string
	i := 0
	for i < len(words) && len(lines) < maxLines {
		cur := ""
		for i < len(words) {
			cand := strings.TrimSpace(cur + " " + words[i])
			if cur != "" && DispW(cand) > w {
				break
			}
			cur = cand
			i++
		}
		if len(lines) == maxLines-1 && i < len(words) {
			cur = Clip(strings.TrimSpace(cur+" "+strings.Join(words[i:], " ")), w)
			i = len(words)
		} else if DispW(cur) > w {
			cur = Clip(cur, w)
		}
		lines = append(lines, cur)
	}
	return lines
}

// SourceName resolves the playback source label from the track's URL/source id.
func SourceName(t protocol.Track) string {
	if t == nil {
		return ""
	}
	url := strings.ToLower(t.Str("PlayUrl"))
	switch {
	case strings.HasPrefix(url, "spotify:"):
		return "Spotify"
	case strings.Contains(url, "tidal"):
		return "TIDAL"
	case strings.Contains(url, "airplay"):
		return "AirPlay"
	}
	src, ok := protocol.Int(t["Current Source"])
	if !ok || src == 0 {
		return ""
	}
	switch src {
	case 1:
		return "AirPlay"
	case 2:
		return "DLNA"
	case 3:
		return "Bluetooth"
	case 4:
		return "Spotify"
	case 5:
		return "Line-In"
	case 6:
		return "USB"
	}
	return fmt.Sprintf("Source %d", src)
}

// Quality renders the "Mime · NN kHz" quality line for a track.
func Quality(t protocol.Track) string {
	var bits []string
	if m := t.Str("Mime"); m != "" {
		bits = append(bits, m)
	}
	if sr, ok := protocol.Int(t["SampleRate"]); ok && sr != 0 {
		bits = append(bits, strconv.FormatFloat(float64(sr)/1000, 'g', -1, 64)+" kHz")
	}
	return strings.Join(bits, " · ")
}

// AlbumLine renders "Album · PlaybackSource", dropping the source when it merely
// echoes the track or album title.
func AlbumLine(t protocol.Track) string {
	if t == nil {
		return ""
	}
	src := t.Str("PlaybackSource")
	if src != "" {
		sc := strings.ToLower(strings.TrimSpace(src))
		if sc == strings.ToLower(strings.TrimSpace(t.Str("TrackName"))) ||
			sc == strings.ToLower(strings.TrimSpace(t.Str("Album"))) {
			src = ""
		}
	}
	var parts []string
	if al := t.Str("Album"); al != "" {
		parts = append(parts, al)
	}
	if src != "" {
		parts = append(parts, src)
	}
	return strings.Join(parts, " · ")
}
