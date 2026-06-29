package tui

import (
	"image"
	"image/color"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lucasdaddiego/lp10/internal/artwork"
	"github.com/lucasdaddiego/lp10/internal/protocol"
)

// A cover renders at its true aspect ratio — not stretched to a square box: the art
// block's cell footprint tracks the source shape (a square stays square in display;
// a 2:1 source renders visibly wider). Regression for the stretched-disc fix.
func TestCoverAspectNotStretched(t *testing.T) {
	const url = "https://i.scdn.co/image/0000000000000000000000000000000000000000" // playing_record's cover
	mk := func(w, h int) image.Image {
		im := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				im.Set(x, y, color.RGBA{180, 180, 180, 255})
			}
		}
		return im
	}
	box := func(srcW, srcH int) (w, h int) {
		st := protocol.NewState()
		protocol.ApplyRecord(st, playingRecord())
		st.SetArt(url, mk(srcW, srcH), color.RGBA{}, false)
		m, _, _ := modelWith(st)
		m.sty = newTheme()
		m.sty.trueColor = true
		m.cfg.Art, m.cfg.ArtMode = true, "halfblock"
		m.rows, m.cols, m.cellW, m.cellH = 34, 120, 10, 20 // measured 2:1 cells
		for _, ln := range strings.Split(m.renderDashboard(st.Snap(), time.Time{}, 114, true), "\n") {
			if c := strings.Count(stripANSI(ln), "▀"); c > 0 {
				h++
				if c > w {
					w = c
				}
			}
		}
		return
	}
	if w, h := box(600, 600); h == 0 || w != 2*h { // 2:1 cells: a square display is 2× as many cols as rows
		t.Errorf("square cover should be square in display, got %d×%d cells", w, h)
	}
	if w, h := box(800, 400); h == 0 || w <= 2*h { // a 2:1 cover must render wider than a square would
		t.Errorf("a 2:1 cover should render wider than tall, got %d×%d cells", w, h)
	}
}

func TestArtChoiceResolution(t *testing.T) {
	cases := []struct {
		mode      string
		trueColor bool
		kitty     bool
		want      artRender
	}{
		{"auto", true, true, artKitty},
		{"auto", true, false, artHalf},
		{"auto", false, false, artMotif},
		{"kitty", true, true, artKitty},
		{"kitty", true, false, artKitty},   // explicit override forces kitty even if undetected
		{"kitty", false, false, artKitty},  // ...even without truecolor (degrades in artColumn)
		{"halfblock", true, true, artHalf}, // never kitty even if capable
		{"halfblock", false, false, artMotif},
		{"off", true, true, artMotif},
	}
	for _, tc := range cases {
		m := artModel(t)
		m.cfg.ArtMode = tc.mode
		m.sty.trueColor = tc.trueColor
		m.sty.kittyGraphics = tc.kitty
		if got := m.artChoice(); got != tc.want {
			t.Errorf("mode=%s tc=%v kitty=%v -> %d, want %d", tc.mode, tc.trueColor, tc.kitty, got, tc.want)
		}
	}
	// Art disabled overrides everything.
	m := artModel(t)
	m.cfg.Art = false
	m.sty.kittyGraphics = true
	if m.artChoice() != artMotif {
		t.Error("art disabled should force the motif")
	}
}

// On a Kitty-capable terminal the art column is a placeholder grid carrying the
// transmit escape, and each line still measures exactly the box width.
func TestArtColumnKitty(t *testing.T) {
	m := artModel(t)
	m.cfg.ArtMode = "auto"
	m.sty.kittyGraphics = true
	s := protocol.Snapshot{Track: protocol.Track{"TrackName": "x"}, CoverURL: "http://x/1", Art: fillImg(40, 40, color.RGBA{20, 180, 90, 255})}
	lines := m.artColumn(s, 12, 6)
	if len(lines) != 6 {
		t.Fatalf("got %d lines, want 6", len(lines))
	}
	ph := string(rune(artwork.KittyPlaceholder))
	if !strings.Contains(lines[0], "\x1b_G") {
		t.Error("first line should carry the Kitty transmit escape")
	}
	for i, ln := range lines {
		if !strings.Contains(ln, ph) {
			t.Errorf("line %d has no placeholder cells", i)
		}
		if w := lipgloss.Width(ln); w != 12 {
			t.Errorf("line %d width %d, want 12 (transmit/placeholders must be width-correct)", i, w)
		}
	}
}

// artColumn caches the kitty raster by (url,w,h,mode) and rebuilds — refreshing
// the embedded transmit — only when the cover changes, so a steady cover costs
// nothing per frame and a track change never composites the previous image.
func TestArtColumnKittyCachesAndInvalidates(t *testing.T) {
	m := artModel(t)
	m.cfg.ArtMode = "auto"
	m.sty.kittyGraphics = true

	s1 := protocol.Snapshot{Track: protocol.Track{"TrackName": "x"}, CoverURL: "http://x/1", Art: fillImg(40, 40, color.RGBA{20, 180, 90, 255})}
	line0 := m.artColumn(s1, 12, 6)[0]
	key := m.artKey

	again := m.artColumn(s1, 12, 6) // identical cover -> served from cache
	if m.artKey != key {
		t.Error("cache key changed for an identical cover")
	}
	if again[0] != line0 {
		t.Error("cached art rebuilt for an identical cover")
	}

	s2 := protocol.Snapshot{Track: protocol.Track{"TrackName": "x"}, CoverURL: "http://x/2", Art: fillImg(40, 40, color.RGBA{200, 30, 30, 255})}
	changed := m.artColumn(s2, 12, 6) // new cover -> rebuilt
	if m.artKey == key {
		t.Error("cache key not updated on cover change")
	}
	if changed[0] == line0 {
		t.Error("transmit not refreshed on cover change (a stale image could composite)")
	}
}

// The idle cover slot is state-aware: a sonar ping while disconnected, then the
// calm note motif when connected-but-idle with nothing to recall. Never the
// plasma motif or half-blocks.
func TestArtColumnIdlePlaceholder(t *testing.T) {
	m := artModel(t)
	m.sty.trueColor = true

	// disconnected -> the animated radar sweep (dot glyphs), and it keeps the clock live
	sonar := strings.Join(m.artColumn(protocol.Snapshot{Track: nil, Connected: false}, 16, 8), "\n")
	if !strings.ContainsAny(sonar, "·●") {
		t.Error("disconnected idle should show the radar sweep")
	}
	if !m.sonarLive {
		t.Error("the sonar should keep the frame clock live")
	}
	if strings.Contains(sonar, "▀") {
		t.Error("the sonar is not a half-block raster")
	}

	// connected, nothing playing, no cover to recall -> the calm note motif
	note := strings.Join(m.artColumn(protocol.Snapshot{Track: nil, Connected: true}, 16, 8), "\n")
	if !strings.Contains(note, GL["note"]) && !strings.Contains(note, "●") {
		t.Error("connected idle should show a note motif")
	}
	if strings.Contains(note, "▀") {
		t.Error("the note motif should not be half-blocks")
	}
}

// Connected and idle with a remembered cover -> a dimmed "ghost" of it (a real
// raster — here half-blocks), not the note motif.
func TestArtColumnGhostCover(t *testing.T) {
	m := artModel(t)
	m.sty.trueColor = true
	m.cfg.ArtMode = "halfblock"
	s := protocol.Snapshot{Track: nil, Connected: true,
		LastArt: fillImg(40, 40, color.RGBA{200, 60, 40, 255}), LastCoverURL: "http://x/last"}
	lines := m.artColumn(s, 16, 8)
	if len(lines) != 8 {
		t.Fatalf("ghost should be 8 lines, got %d", len(lines))
	}
	if !strings.Contains(strings.Join(lines, "\n"), "▀") {
		t.Error("a remembered cover should render as a ghost raster (half-blocks)")
	}
}

// boxArt adds a one-cell frame: +2 lines, +2 columns, with the corner glyphs.
func TestBoxArt(t *testing.T) {
	m := artModel(t)
	framed := m.boxArt([]string{"abcd", "efgh"}, 4)
	if len(framed) != 4 {
		t.Fatalf("got %d lines, want 4 (2 content + top/bottom)", len(framed))
	}
	if !strings.Contains(framed[0], "╭") || !strings.Contains(framed[3], "╰") {
		t.Error("missing top/bottom frame corners")
	}
	if w := lipgloss.Width(framed[1]); w != 6 {
		t.Errorf("framed content width %d, want 6 (4 + 2 border)", w)
	}
}

// The frame tick advances the motif only while it's both on screen (motifLive)
// and playing, and always reschedules itself so the animation clock keeps running.
func TestFrameTickAdvancesOnlyWhilePlaying(t *testing.T) {
	m, _, _ := makeModel(t) // seeded playing (Playing == 0)
	if m.st.Snap().Playing != 0 {
		t.Fatal("setup: expected playing")
	}
	m.motifLive = true // the plasma is on screen this frame
	before := m.frame
	_, cmd := m.Update(frameMsg{})
	if m.frame != before+1 {
		t.Errorf("playing+motif: frame %d -> %d, want +1", before, m.frame)
	}
	if cmd == nil {
		t.Error("frame tick should reschedule itself")
	}

	// album art shown instead of the motif: the frame clock idles even while playing
	m.motifLive = false
	held := m.frame
	m.Update(frameMsg{})
	if m.frame != held {
		t.Errorf("motif not live: frame advanced to %d, want held at %d", m.frame, held)
	}

	// pause: optimistic toggle flips Playing away from 0; the motif then freezes
	m.motifLive = true
	m.key(kr(' '))
	if m.st.Snap().Playing == 0 {
		t.Fatal("setup: expected paused after toggle")
	}
	frozen := m.frame
	m.Update(frameMsg{})
	if m.frame != frozen {
		t.Errorf("paused: frame advanced to %d, want frozen at %d", m.frame, frozen)
	}
}

// The logic tick advances the marquee and never advances the motif frame.
func TestLogicTickAdvancesMarquee(t *testing.T) {
	m, _, _ := makeModel(t)
	scroll, frame := m.scroll, m.frame
	var msg tea.Msg = logicMsg{}
	if _, cmd := m.Update(msg); cmd == nil {
		t.Error("logic tick should reschedule itself")
	}
	if m.scroll != scroll+1 {
		t.Errorf("scroll %d -> %d, want +1", scroll, m.scroll)
	}
	if m.frame != frame {
		t.Errorf("logic tick advanced the motif frame (%d -> %d)", frame, m.frame)
	}
}

func fillImg(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func artModel(t *testing.T) *model {
	t.Helper()
	m := newModel(protocol.NewState(), defaultCfg(), make(chan *protocol.Command, 1), nil)
	m.cfg.Art = true
	m.cfg.ArtMode = "halfblock" // deterministic baseline (newTheme detects the real terminal)
	m.sty = newTheme()
	m.sty.kittyGraphics = false // tests opt into kitty explicitly
	return m
}

// On a truecolor terminal with a loaded cover, the art column is a half-block
// raster (▀), not the procedural motif (█).
func TestArtColumnHalfBlockWhenTrueColor(t *testing.T) {
	m := artModel(t)
	m.sty.trueColor = true
	s := protocol.Snapshot{Track: protocol.Track{"TrackName": "x"}, CoverURL: "http://x/1", Art: fillImg(12, 12, color.RGBA{30, 120, 200, 255})}
	lines := m.artColumn(s, 12, 6)
	if len(lines) != 6 {
		t.Fatalf("got %d lines, want 6", len(lines))
	}
	if !strings.Contains(strings.Join(lines, "\n"), "▀") {
		t.Error("expected half-block art, got none")
	}
}

// Without truecolor, with art disabled, or with no cover loaded, the column
// falls back to the motif — never half-blocks.
func TestArtColumnFallsBackToMotif(t *testing.T) {
	img := fillImg(12, 12, color.RGBA{30, 120, 200, 255})
	cases := []struct {
		name      string
		trueColor bool
		art       bool
		snap      protocol.Snapshot
	}{
		{"lesser terminal", false, true, protocol.Snapshot{Track: protocol.Track{"TrackName": "x"}, CoverURL: "u", Art: img}},
		{"art disabled", true, false, protocol.Snapshot{Track: protocol.Track{"TrackName": "x"}, CoverURL: "u", Art: img}},
		{"no cover loaded", true, true, protocol.Snapshot{Track: protocol.Track{"TrackName": "x"}, CoverURL: "u", Art: nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := artModel(t)
			m.sty.trueColor = tc.trueColor
			m.cfg.Art = tc.art
			joined := strings.Join(m.artColumn(tc.snap, 12, 6), "\n")
			if strings.Contains(joined, "▀") {
				t.Error("should have used the motif, not half-blocks")
			}
			if !strings.Contains(joined, "█") {
				t.Error("expected motif full-blocks")
			}
		})
	}
}
