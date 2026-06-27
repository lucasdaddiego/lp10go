package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// Under a CJK locale (amb==2), the East-Asian-Ambiguous UI glyphs lp10 draws
// must still measure width-1 — matching lipgloss (which renders/pads) and a
// modern terminal — so DispW and lipgloss never disagree and tear the layout.
func TestAmbiguousGlyphsMeasureNarrowUnderCJK(t *testing.T) {
	defer func(orig int) { amb = orig }(amb)
	amb = 2 // simulate a CJK locale
	for _, r := range []rune{'●', '○', '·', '━', '─', '┃', '│', '█', '░', '▀', '…', '▁', '▇',
		'╭', '╮', '╰', '╯', '┏', '┓', '┊'} { // cover-frame corners, idle-motif beam, mute column
		if got := charW(r); got != 1 {
			t.Errorf("charW(%q) = %d under amb=2, want 1 (must match lipgloss)", r, got)
		}
	}
	if charW('漢') != 2 {
		t.Error("a genuinely wide glyph must still be width 2")
	}
	// the two width oracles must agree on a real UI line mixing these glyphs
	line := "● LP10 · Living  ━━━●──── ┃ █░▀ ▁▂▇"
	if DispW(line) != lipgloss.Width(line) {
		t.Errorf("DispW=%d but lipgloss.Width=%d — they must agree", DispW(line), lipgloss.Width(line))
	}
}

func TestSpotifySearch(t *testing.T) {
	if got := spotifySearch("De Música Ligera Soda Stereo"); got != "https://open.spotify.com/search/De%20M%C3%BAsica%20Ligera%20Soda%20Stereo" {
		t.Errorf("unexpected url: %q", got)
	}
	if spotifySearch("   ") != "" {
		t.Error("blank query should yield no url")
	}
}

func TestOsc8ZeroWidth(t *testing.T) {
	plain := "hello"
	linked := osc8("https://open.spotify.com/search/x", plain)
	if !strings.HasPrefix(linked, "\x1b]8;;") || !strings.HasSuffix(linked, "\x1b]8;;\x1b\\") {
		t.Errorf("not a well-formed OSC 8 link: %q", linked)
	}
	if lipgloss.Width(linked) != lipgloss.Width(plain) {
		t.Errorf("link changed display width: %d vs %d", lipgloss.Width(linked), lipgloss.Width(plain))
	}
	if osc8("", plain) != plain {
		t.Error("empty url should pass text through unwrapped")
	}
}

// The OSC 8 wrapper must never inflate a line's measured width, even when the
// title overflows and is marquee-windowed — the path that runs dispWindow and
// then wraps the result. lipgloss must still see each line as <= the column.
func TestMetaLinesHyperlinkWidthOnOverflow(t *testing.T) {
	m, _, _ := makeModel(t)
	m.sty = newTheme()
	m.scroll = 7 // mid-marquee, exercises dispWindow edge-straddling
	const w = 8  // narrower than every metadata line -> all overflow and scroll
	for i, ln := range m.metaLines(m.st.Snap(), w) {
		if got := lipgloss.Width(ln); got > w {
			t.Errorf("line %d width %d exceeds column %d: %q", i, got, w, ln)
		}
	}
}

// The title line carries a clickable Spotify link, and the link must not inflate
// the line's display width (column alignment depends on it).
func TestMetaLinesHyperlinked(t *testing.T) {
	m, _, _ := makeModel(t)
	m.sty = newTheme()
	lines := m.metaLines(m.st.Snap(), 40)
	if len(lines) < 2 {
		t.Fatalf("got %d meta lines", len(lines))
	}
	if !strings.Contains(lines[0], "\x1b]8;;https://open.spotify.com/search/") {
		t.Errorf("title not hyperlinked: %q", lines[0])
	}
	if w := lipgloss.Width(lines[0]); w > 40 {
		t.Errorf("hyperlinked title width %d exceeds column 40", w)
	}
}
