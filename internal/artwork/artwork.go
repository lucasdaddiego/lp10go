// Package artwork fetches album-cover images and rasterizes them for the
// terminal. The baseline renderer is a 24-bit half-block raster (HalfBlock):
// each character cell paints its top pixel as the foreground and its bottom
// pixel as the background of an upper-half-block glyph (▀), so one text row
// shows two image rows. It needs only a truecolor terminal — no graphics
// protocol — so it composes cleanly with a cell-diffing renderer and works
// everywhere Ghostty does (the Kitty true-pixel path layers on top in tui).
//
// Fetches are cached on disk (raw bytes, keyed by URL hash) so a re-seen cover
// needs no network and the last-played art is available offline and for an
// instant first paint, mirroring the snapshot-preload philosophy elsewhere.
package artwork

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"  // register decoders used by image.Decode
	_ "image/jpeg" // ""
	_ "image/png"  // ""
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lucasdaddiego/lp10/internal/atomicfile"
)

const (
	// maxArtBytes bounds a cover download so a hostile or misbehaving endpoint
	// can't stream unbounded data into memory. Spotify covers are a few tens of KB.
	maxArtBytes = 8 << 20
	// maxArtPixels caps the *decoded* dimensions (Width×Height): the byte cap above
	// bounds the compressed payload, but a tiny crafted PNG can declare enormous
	// dimensions and force a multi-GB pixel buffer. A 12×6-cell box never needs
	// more than a handful of megapixels, so reject anything larger up front.
	maxArtPixels = 16 << 20
)

// ErrUndecodable marks a cover whose bytes are not a supported/decodable image
// (e.g. WebP, or oversized). It's deterministic for a given url, so the worker
// can give up on it rather than re-downloading on every retry tick.
var ErrUndecodable = errors.New("art: undecodable image")

var httpClient = &http.Client{}

// Get returns the decoded cover for url. It serves from the on-disk cache under
// dir (raw bytes keyed by the url hash) when present, else fetches once over
// HTTP(S), decodes (gif/jpeg/png), and populates the cache. dir == "" disables
// the cache (network only). The caller's ctx bounds the network wait. A url with
// a non-http(s) scheme, or an image that fails to decode or exceeds maxArtPixels,
// returns ErrUndecodable.
func Get(ctx context.Context, rawurl, dir string) (image.Image, error) {
	if u, err := url.Parse(rawurl); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("%w: scheme %q", ErrUndecodable, rawurl)
	}
	rawurl = canonicalCover(rawurl) // prefer the full-size source over a thumbnail
	if raw, ok := loadCache(dir, rawurl); ok {
		if img, err := decode(raw); err == nil {
			return img, nil
		}
		// a corrupt/undecodable cache entry falls through to a refetch
	}
	raw, err := fetch(ctx, rawurl)
	if err != nil {
		return nil, err // transient (network / HTTP status): caller may retry
	}
	img, err := decode(raw)
	if err != nil {
		return nil, err // ErrUndecodable: caller should not retry this url
	}
	saveCache(dir, rawurl, raw)
	return img, nil
}

// decode validates an image's declared size cheaply (DecodeConfig reads only the
// header) before the full decode, so a decompression bomb is rejected without
// allocating its pixel buffer. Returns ErrUndecodable on any failure.
func decode(raw []byte) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUndecodable, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxArtPixels {
		return nil, fmt.Errorf("%w: %dx%d", ErrUndecodable, cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUndecodable, err)
	}
	return img, nil
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("art: http %d for %s", resp.StatusCode, url)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxArtBytes))
}

// PruneCache keeps at most `keep` cover files in dir (the most-recently-modified),
// removing older ones so the cache can't grow without bound. Best effort: any IO
// error just leaves the cache as-is. A non-positive keep, or "" dir, is a no-op.
func PruneCache(dir string, keep int) {
	if dir == "" || keep <= 0 {
		return
	}
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) <= keep {
		return
	}
	type f struct {
		path string
		mod  int64
	}
	files := make([]f, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, f{filepath.Join(dir, e.Name()), info.ModTime().UnixNano()})
	}
	if len(files) <= keep {
		return
	}
	slices.SortFunc(files, func(a, b f) int { return cmp.Compare(b.mod, a.mod) }) // newest first
	for _, old := range files[keep:] {
		_ = os.Remove(old.path)
	}
}

// canonicalCover upgrades a Spotify album-cover URL to its full-size (640px)
// variant, so we fetch and cache the largest available source regardless of the
// thumbnail size the device reported (the LP10 sometimes hands back a small one).
// The 8 hex digits after the "ab67616d" album-image tag encode the size; the
// "0000b273" master is 640px. Non-Spotify or non-album URLs pass through.
func canonicalCover(u string) string {
	const tag = "/image/ab67616d"
	i := strings.Index(u, tag)
	if i < 0 {
		return u
	}
	j := i + len(tag)
	if len(u) < j+8 {
		return u
	}
	return u[:j] + "0000b273" + u[j+8:]
}

func cacheFile(dir, url string) string {
	if dir == "" {
		return ""
	}
	// sha1 as a content-addressed filename only — a stable, short cache key, not
	// a security boundary (a gosec/CodeQL "weak hash" hit here is a false positive).
	sum := sha1.Sum([]byte(url))
	return filepath.Join(dir, hex.EncodeToString(sum[:]))
}

func loadCache(dir, url string) ([]byte, bool) {
	p := cacheFile(dir, url)
	if p == "" {
		return nil, false
	}
	b, err := os.ReadFile(p)
	if err != nil || len(b) == 0 {
		return nil, false
	}
	return b, true
}

func saveCache(dir, url string, raw []byte) {
	if p := cacheFile(dir, url); p != "" {
		_ = atomicfile.Write(p, raw) // best effort; a lost cache write just refetches
	}
}

// HalfBlock rasterizes img into a wCells×hCells grid of upper-half-block glyphs.
// Each cell's foreground is the image's top pixel and its background the bottom
// pixel, so the cell shows two stacked image rows. The result has hCells lines,
// each exactly wCells display columns wide (the trailing reset is zero-width).
// Emits 24-bit SGR — use only on a truecolor terminal.
func HalfBlock(img image.Image, wCells, hCells int) []string {
	if img == nil || wCells <= 0 || hCells <= 0 {
		return nil
	}
	px := downscale(img, wCells, hCells*2)
	lines := make([]string, hCells)
	var b strings.Builder
	for cy := range hCells {
		b.Reset()
		for cx := range wCells {
			tr, tg, tb := rgbAt(px, cx, cy*2)
			br, bg, bb := rgbAt(px, cx, cy*2+1)
			fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%d;48;2;%d;%d;%dm▀", tr, tg, tb, br, bg, bb)
		}
		b.WriteString("\x1b[0m")
		lines[cy] = b.String()
	}
	return lines
}

func rgbAt(px *image.RGBA, x, y int) (uint8, uint8, uint8) {
	i := px.PixOffset(x, y)
	return px.Pix[i], px.Pix[i+1], px.Pix[i+2]
}

// Dominant picks a representative *vivid* colour from img, for tinting the UI to
// the album. It samples a downscaled copy into a saturation-weighted hue
// histogram (so a small splash of strong colour can win over a large muddy
// background) and returns the weighted-average colour of the most prominent hue.
// Pixels that are near-black, near-white, or washed-out carry no usable hue and
// are skipped. Reports ok=false when nothing vivid remains (e.g. a greyscale
// cover), so the caller keeps its default accent rather than tinting to mud.
func Dominant(img image.Image) (color.RGBA, bool) {
	if img == nil {
		return color.RGBA{}, false
	}
	const (
		grid    = 48 // sample resolution (box-averaged, so detail below this blends)
		buckets = 24 // hue bins (15° each)
	)
	px := downscale(img, grid, grid)
	var weight, rs, gs, bs [buckets]float64
	for y := range grid {
		for x := range grid {
			r, g, b := rgbAt(px, x, y)
			h, s, l := RGBToHSL(r, g, b)
			if l < 0.12 || l > 0.92 || s < 0.18 {
				continue // no meaningful hue: would only bias the average toward grey
			}
			bk := int(h/360*buckets) % buckets // h ∈ [0,360) ⇒ bk ∈ [0,buckets)
			w := s * (1 - math.Abs(2*l-1))     // favour saturated mid-tones
			weight[bk] += w
			rs[bk] += float64(r) * w
			gs[bk] += float64(g) * w
			bs[bk] += float64(b) * w
		}
	}
	best := 0
	for i := 1; i < buckets; i++ {
		if weight[i] > weight[best] {
			best = i
		}
	}
	if weight[best] == 0 {
		return color.RGBA{}, false
	}
	w := weight[best]
	return color.RGBA{
		R: uint8(rs[best]/w + 0.5), // round (the average is in [0,255])
		G: uint8(gs[best]/w + 0.5),
		B: uint8(bs[best]/w + 0.5),
		A: 0xff,
	}, true
}

// RGBToHSL converts an 8-bit RGB triple to hue (0–360°), saturation, lightness
// (both 0–1). Shared by Dominant (binning pixels by hue) and the TUI theme
// (recolouring the meter to a cover's hue), so the two stay in lock-step.
func RGBToHSL(r, g, b uint8) (h, s, l float64) {
	rf, gf, bf := float64(r)/255, float64(g)/255, float64(b)/255
	maxc := max(rf, gf, bf)
	minc := min(rf, gf, bf)
	l = (maxc + minc) / 2
	d := maxc - minc
	if d == 0 {
		return 0, 0, l // grey
	}
	if l > 0.5 {
		s = d / (2 - maxc - minc)
	} else {
		s = d / (maxc + minc)
	}
	switch maxc {
	case rf:
		h = math.Mod((gf-bf)/d, 6)
	case gf:
		h = (bf-rf)/d + 2
	default:
		h = (rf-gf)/d + 4
	}
	if h *= 60; h < 0 {
		h += 360
	}
	return h, s, l
}

// Ghost returns a dimmed, desaturated copy of img for the idle "last cover"
// backdrop: each pixel is pulled most of the way toward its own grey (luma) and
// then darkened, so the cover reads as a faint memory of what was last playing
// rather than the active art. The alpha is forced opaque.
func Ghost(img image.Image) image.Image {
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	const (
		keep = 0.32 // fraction of original chroma retained (rest -> grey)
		dim  = 0.42 // overall brightness after desaturation
	)
	mix := func(c, luma float64) uint8 {
		v := (keep*c + (1-keep)*luma) * dim
		if v < 0 {
			return 0
		}
		if v > 255 {
			return 255
		}
		return uint8(v)
	}
	for y := range b.Dy() {
		for x := range b.Dx() {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA() // 16-bit
			rf, gf, bf := float64(r>>8), float64(g>>8), float64(bl>>8)
			luma := 0.299*rf + 0.587*gf + 0.114*bf
			dst.Set(x, y, color.RGBA{mix(rf, luma), mix(gf, luma), mix(bf, luma), 0xff})
		}
	}
	return dst
}

// downscale resamples src to w×h by box averaging (area resampling): every
// destination cell averages the source pixels that fall within it. This is
// only ever used to shrink (cover → a small cell grid), where box averaging
// gives clean results with no dependency. Each source pixel is visited once.
func downscale(src image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || sw <= 0 || sh <= 0 {
		return dst
	}
	for dy := range h {
		y0 := b.Min.Y + dy*sh/h
		y1 := b.Min.Y + (dy+1)*sh/h
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for dx := range w {
			x0 := b.Min.X + dx*sw/w
			x1 := b.Min.X + (dx+1)*sw/w
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var rs, gs, bs, n uint64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					r, g, bl, _ := src.At(xx, yy).RGBA() // 16-bit per channel
					rs += uint64(r)
					gs += uint64(g)
					bs += uint64(bl)
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			dst.Set(dx, dy, color.RGBA{
				R: uint8((rs / n) >> 8),
				G: uint8((gs / n) >> 8),
				B: uint8((bs / n) >> 8),
				A: 0xff,
			})
		}
	}
	return dst
}
