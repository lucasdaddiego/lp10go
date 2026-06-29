package artwork

// coverage_test.go drives the remaining branches of the artwork package that the
// behaviour-focused suites (artwork_test.go, kitty_test.go, hardening_test.go)
// don't already exercise. It only ADDS tests (all prefixed TestCov_) and reuses
// the helpers defined there (solid, pngBytes, tinyPNGHeader) — same package.

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// --- decode -----------------------------------------------------------------

// A header whose dimensions are sane (so the bomb guard passes) but that has no
// pixel data: DecodeConfig succeeds, the full image.Decode fails — exercising the
// post-config decode-failure path, distinct from the header-parse failure that
// garbage bytes already cover.
func TestCov_DecodeFailsAfterConfigOK(t *testing.T) {
	raw := tinyPNGHeader(8, 8) // valid signature + IHDR(8x8), but no IDAT/IEND
	if _, err := decode(raw); !errors.Is(err, ErrUndecodable) {
		t.Fatalf("decode of header-only PNG: err=%v, want ErrUndecodable", err)
	}
	// sanity: a complete small PNG still decodes through the same function
	if _, err := decode(pngBytes(t, solid(6, 6, color.RGBA{4, 5, 6, 255}))); err != nil {
		t.Errorf("decode of a valid PNG: %v", err)
	}
}

// --- fetch ------------------------------------------------------------------

// http.NewRequestWithContext fails before any network use when the URL can't be
// parsed into a request (fetch is called directly, bypassing Get's scheme check).
func TestCov_FetchRequestBuildError(t *testing.T) {
	if _, err := fetch(context.Background(), "://nope"); err == nil {
		t.Fatal("fetch with an unparseable URL should error at request build")
	}
}

// httpClient.Do fails when the context is already cancelled, so the transport
// error path (distinct from a non-200 status) is covered.
func TestCov_FetchTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(pngBytes(t, solid(2, 2, color.RGBA{7, 7, 7, 255})))
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Do must refuse the cancelled context
	if _, err := fetch(ctx, srv.URL); err == nil {
		t.Fatal("fetch with a cancelled context should surface a transport error")
	}
}

// --- canonicalCover ---------------------------------------------------------

// A Spotify album-image URL truncated to fewer than the 8 size hex digits passes
// through untouched (the slice that would be rewritten doesn't exist).
func TestCov_CanonicalCoverTooShort(t *testing.T) {
	in := "https://i.scdn.co/image/ab67616d12" // only 2 chars after the tag
	if got := canonicalCover(in); got != in {
		t.Errorf("canonicalCover(%q) = %q, want unchanged", in, got)
	}
	// a full-length one is still rewritten to the 640px master (guards the
	// too-short branch isn't accidentally swallowing valid URLs)
	full := "https://i.scdn.co/image/ab67616d00001e02deadbeefdeadbeefdeadbeef"
	want := "https://i.scdn.co/image/ab67616d0000b273deadbeefdeadbeefdeadbeef"
	if got := canonicalCover(full); got != want {
		t.Errorf("canonicalCover(%q) = %q, want %q", full, got, want)
	}
}

// --- cacheFile / loadCache / saveCache --------------------------------------

// With no cache directory configured every cache primitive is an inert no-op:
// cacheFile yields "", loadCache reports a miss, saveCache writes nothing.
func TestCov_CacheDisabledWhenDirEmpty(t *testing.T) {
	const u = "https://example.com/cover.png"
	if p := cacheFile("", u); p != "" {
		t.Errorf("cacheFile(\"\", _) = %q, want empty", p)
	}
	if b, ok := loadCache("", u); ok || b != nil {
		t.Errorf("loadCache(\"\", _) = (%v,%v), want (nil,false)", b, ok)
	}
	saveCache("", u, []byte("data")) // must not panic and writes nothing
}

// A full save -> load round-trip returns the exact bytes back, and a load of a
// path that was never written reports a miss.
func TestCov_CacheRoundTripAndMiss(t *testing.T) {
	dir := t.TempDir()
	const u = "https://i.scdn.co/image/roundtrip"
	want := []byte("\x89PNG-ish-bytes")
	saveCache(dir, u, want)
	got, ok := loadCache(dir, u)
	if !ok || string(got) != string(want) {
		t.Fatalf("round-trip = (%q,%v), want (%q,true)", got, ok, want)
	}
	if _, ok := loadCache(dir, "https://i.scdn.co/image/never-written"); ok {
		t.Error("loadCache of a missing entry should report a miss")
	}
}

// When the temp write can't be created (the cache dir doesn't exist) saveCache
// fails silently, leaving nothing behind — the caller just refetches next time.
func TestCov_SaveCacheTempWriteFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	const u = "https://example.com/x.png"
	saveCache(dir, u, []byte("data")) // parent dir absent -> WriteFile errors -> no-op
	if _, err := os.Stat(cacheFile(dir, u)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a failed save must leave no cache file, stat err=%v", err)
	}
}

// --- PruneCache -------------------------------------------------------------

// An unreadable / nonexistent directory and a directory with fewer entries than
// `keep` are both no-ops (the early return after ReadDir).
func TestCov_PruneCacheEarlyReturns(t *testing.T) {
	// ReadDir error: a path that doesn't exist
	PruneCache(filepath.Join(t.TempDir(), "no-such-dir"), 5)

	// fewer entries than keep: nothing is removed
	dir := t.TempDir()
	p := filepath.Join(dir, "only")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	PruneCache(dir, 5)
	if _, err := os.Stat(p); err != nil {
		t.Errorf("prune with fewer files than keep removed %q: %v", p, err)
	}
}

// Subdirectories are skipped, and when the regular-file count is within `keep`
// (even though the raw entry count exceeds it) nothing is pruned.
func TestCov_PruneCacheSkipsDirsAndKeepsWhenFilesUnderKeep(t *testing.T) {
	dir := t.TempDir()
	files := []string{"a", "b"}
	for _, n := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"sub1", "sub2"} {
		if err := os.Mkdir(filepath.Join(dir, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// 4 entries > keep(2) passes the first gate; after skipping the 2 dirs only
	// 2 files remain (<= keep) so nothing is removed.
	PruneCache(dir, 2)
	ents, _ := os.ReadDir(dir)
	if len(ents) != 4 {
		t.Errorf("entries after prune = %d, want 4 (no removal)", len(ents))
	}
	for _, n := range files {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("file %q should survive: %v", n, err)
		}
	}
}

// --- RGBToHSL ---------------------------------------------------------------

// Three RGB triples drive the branches the existing tests miss: a light colour
// (lightness > 0.5 saturation formula), a green-max pixel (the green switch arm),
// and a red-max pixel whose hue goes negative and must wrap by +360. The grey
// path (max == min) is asserted too for completeness.
func TestCov_RGBToHSLBranches(t *testing.T) {
	// grey: no chroma
	if h, s, _ := RGBToHSL(128, 128, 128); h != 0 || s != 0 {
		t.Errorf("grey: h=%v s=%v, want 0,0", h, s)
	}
	// light colour: lightness above 0.5 -> the 2-maxc-minc saturation arm
	if h, s, l := RGBToHSL(255, 200, 200); l <= 0.5 || s <= 0 || h != 0 {
		t.Errorf("light pink: h=%.1f s=%.3f l=%.3f, want l>0.5,s>0,h=0", h, s, l)
	}
	// green is the max channel -> hue lands in the green sextant (~135)
	if h, _, _ := RGBToHSL(40, 200, 80); h < 90 || h > 160 {
		t.Errorf("green-max hue = %.1f, want ~120-150", h)
	}
	// red max with blue > green -> raw hue is negative and wraps to ~330
	if h, _, _ := RGBToHSL(255, 0, 128); h < 180 {
		t.Errorf("wrapped hue = %.1f, want >180 (negative hue wrapped by +360)", h)
	}
}

// --- Ghost ------------------------------------------------------------------

// Ghost forces opaque alpha and pulls every pixel toward grey-and-dim; a vivid
// red becomes much darker. (The mix() clamp branches are unreachable for 8-bit
// input — see the residual notes — so only the main path is asserted.)
func TestCov_GhostDimsAndForcesOpaque(t *testing.T) {
	src := solid(4, 4, color.RGBA{240, 30, 30, 200}) // semi-transparent vivid red
	g := Ghost(src)
	r, _, _, a := g.At(0, 0).RGBA()
	if a>>8 != 0xff {
		t.Errorf("ghost alpha = %d, want 255 (forced opaque)", a>>8)
	}
	if r>>8 >= 240 {
		t.Errorf("ghost red = %d, want dimmed below the source 240", r>>8)
	}
}

// --- downscale --------------------------------------------------------------

// The dimension guard returns an empty destination rather than ranging over a
// non-positive axis (w<=0, h<=0, or an empty source).
func TestCov_DownscaleGuards(t *testing.T) {
	src := solid(4, 4, color.RGBA{10, 20, 30, 255})
	if g := downscale(src, 0, 4); g.Bounds().Dx() != 0 {
		t.Errorf("downscale w=0 -> Dx=%d, want 0", g.Bounds().Dx())
	}
	if g := downscale(src, 4, 0); g.Bounds().Dy() != 0 {
		t.Errorf("downscale h=0 -> Dy=%d, want 0", g.Bounds().Dy())
	}
	// empty source (sw<=0): returns the (zeroed) dst without sampling
	empty := image.NewRGBA(image.Rect(0, 0, 0, 0))
	if g := downscale(empty, 4, 4); g.Bounds().Dx() != 4 {
		t.Errorf("downscale of empty src -> Dx=%d, want 4 (zeroed dst)", g.Bounds().Dx())
	}
}

// --- resample ---------------------------------------------------------------

// The dimension guard returns an empty destination for a non-positive axis.
func TestCov_ResampleGuard(t *testing.T) {
	if g := resample(solid(4, 4, color.RGBA{1, 1, 1, 255}), 0, 4); g.Bounds().Dx() != 0 {
		t.Errorf("resample w=0 -> Dx=%d, want 0", g.Bounds().Dx())
	}
}

// --- sizeForPlacement -------------------------------------------------------

// An empty source returns unchanged even with a positive footprint (nothing to
// scale); and footprints so lopsided that the fitted edge rounds below one pixel
// are clamped to 1px, then centred onto the full footprint canvas.
func TestCov_SizeForPlacementEdgeCases(t *testing.T) {
	empty := image.NewRGBA(image.Rect(0, 0, 0, 0))
	if g := sizeForPlacement(empty, 100, 100); g.Bounds() != empty.Bounds() {
		t.Errorf("empty source -> bounds %v, want unchanged %v", g.Bounds(), empty.Bounds())
	}
	// 1x1000 source into a 1000x1 footprint: the width fits to 0 -> clamped to 1,
	// then centred on the 1000x1 canvas.
	tall := solid(1, 1000, color.RGBA{5, 5, 5, 255})
	if g := sizeForPlacement(tall, 1000, 1); g.Bounds().Dx() != 1000 || g.Bounds().Dy() != 1 {
		t.Errorf("dw<1 clamp -> %v, want 1000x1 canvas", g.Bounds())
	}
	// 1000x1 source into a 1x1000 footprint: the height fits to 0 -> clamped to 1.
	wide := solid(1000, 1, color.RGBA{5, 5, 5, 255})
	if g := sizeForPlacement(wide, 1, 1000); g.Bounds().Dx() != 1 || g.Bounds().Dy() != 1000 {
		t.Errorf("dh<1 clamp -> %v, want 1x1000 canvas", g.Bounds())
	}
}

// --- fit --------------------------------------------------------------------

// fit's longest-edge selection and both clamp directions: a portrait image's
// height drives the longest edge, an over-large image is reduced to maxPx, and an
// image already inside [minPx,maxPx] is returned untouched.
func TestCov_FitBranches(t *testing.T) {
	// portrait: height is the longest edge; below minPx -> upscaled to minPx tall
	if g := fit(solid(40, 80, color.RGBA{}), kittyMinPx, kittyMaxPx); g.Bounds().Dy() != kittyMinPx {
		t.Errorf("portrait fit Dy = %d, want %d", g.Bounds().Dy(), kittyMinPx)
	}
	// longest edge above maxPx -> reduced so the long side equals maxPx
	if g := fit(solid(2100, 100, color.RGBA{}), kittyMinPx, kittyMaxPx); g.Bounds().Dx() != kittyMaxPx {
		t.Errorf("oversized fit Dx = %d, want %d", g.Bounds().Dx(), kittyMaxPx)
	}
	// already within range (target == long) -> returned unchanged
	mid := solid(700, 700, color.RGBA{})
	if g := fit(mid, kittyMinPx, kittyMaxPx); g.Bounds() != mid.Bounds() {
		t.Errorf("in-range fit -> %v, want unchanged %v", g.Bounds(), mid.Bounds())
	}
}

// fit's enlargement path must route through bilinear resample (smooth), not
// box-average downscale (which degenerates to a blocky nearest-neighbour pick when
// upscaling). A non-uniform sub-minPx image makes the two diverge, so fit's output
// must equal resample's byte-for-byte. Regression for the blocky-upscale fix.
func TestCov_FitEnlargesViaResample(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			src.Set(x, y, color.RGBA{uint8(x * 60), uint8(y * 60), 0, 255})
		}
	}
	got, ok := fit(src, kittyMinPx, kittyMaxPx).(*image.RGBA) // long=4 < minPx -> enlarge
	if !ok {
		t.Fatal("fit should return a freshly scaled *image.RGBA when enlarging")
	}
	want := resample(src, kittyMinPx, kittyMinPx)
	if !bytes.Equal(got.Pix, want.Pix) {
		t.Error("fit enlargement must use bilinear resample, not box-average downscale")
	}
}

// KittyImage degrades to ("", nil) — and the caller falls back to the half-block /
// motif path — when png.Encode can't encode the placement. A non-nil zero-area
// image (cols/rows valid) reaches png.Encode unscaled and fails its size check.
func TestCov_KittyImageEncodeFailureDegrades(t *testing.T) {
	empty := image.NewRGBA(image.Rect(0, 0, 0, 0)) // non-nil, zero area
	if tr, ls := KittyImage(empty, 1, 1, 1, 0, 0); tr != "" || ls != nil {
		t.Errorf("zero-area image should degrade to (\"\", nil), got (%q, %v)", tr, ls)
	}
}
