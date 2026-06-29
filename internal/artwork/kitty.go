package artwork

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
)

// KittyPlaceholder is the Unicode code point the Kitty graphics protocol uses as
// an image placeholder cell: the terminal composites a transmitted image onto
// runs of these, addressed by row/column diacritics. It measures one display
// column wide (verified), so a grid of them aligns like any other text.
const KittyPlaceholder = 0x10EEEE

// A virtual placement is drawn at the transmitted image's *native* pixel size,
// clipped to its cell footprint (Ghostty does not scale it up to the cell box).
// So when the caller knows the footprint's device pixels (pxW×pxH) we size the
// PNG to exactly that — an undersized source otherwise leaves a gap in the frame,
// an oversized one is clipped. kittyMin/MaxPx bound the longest-edge fallback
// used when the cell size is unknown; kittyMaxPx also caps the footprint so a
// pathological box can't blow up the payload.
const (
	kittyMinPx = 640
	kittyMaxPx = 2048
)

// kittyDiacritics is the canonical Kitty row/column diacritic table: the Nth
// entry (0-based) is the combining mark that encodes value N. Taken verbatim
// from kitty's gen/rowcolumn-diacritics.txt (297 entries, derived from
// UnicodeData 6.0.0 Mn;230 combining marks). A placeholder cell carries its row
// diacritic then its column diacritic; the image id rides the foreground color.
var kittyDiacritics = []rune{
	0x0305, 0x030D, 0x030E, 0x0310, 0x0312, 0x033D, 0x033E, 0x033F, 0x0346, 0x034A, 0x034B, 0x034C,
	0x0350, 0x0351, 0x0352, 0x0357, 0x035B, 0x0363, 0x0364, 0x0365, 0x0366, 0x0367, 0x0368, 0x0369,
	0x036A, 0x036B, 0x036C, 0x036D, 0x036E, 0x036F, 0x0483, 0x0484, 0x0485, 0x0486, 0x0487, 0x0592,
	0x0593, 0x0594, 0x0595, 0x0597, 0x0598, 0x0599, 0x059C, 0x059D, 0x059E, 0x059F, 0x05A0, 0x05A1,
	0x05A8, 0x05A9, 0x05AB, 0x05AC, 0x05AF, 0x05C4, 0x0610, 0x0611, 0x0612, 0x0613, 0x0614, 0x0615,
	0x0616, 0x0617, 0x0657, 0x0658, 0x0659, 0x065A, 0x065B, 0x065D, 0x065E, 0x06D6, 0x06D7, 0x06D8,
	0x06D9, 0x06DA, 0x06DB, 0x06DC, 0x06DF, 0x06E0, 0x06E1, 0x06E2, 0x06E4, 0x06E7, 0x06E8, 0x06EB,
	0x06EC, 0x0730, 0x0732, 0x0733, 0x0735, 0x0736, 0x073A, 0x073D, 0x073F, 0x0740, 0x0741, 0x0743,
	0x0745, 0x0747, 0x0749, 0x074A, 0x07EB, 0x07EC, 0x07ED, 0x07EE, 0x07EF, 0x07F0, 0x07F1, 0x07F3,
	0x0816, 0x0817, 0x0818, 0x0819, 0x081B, 0x081C, 0x081D, 0x081E, 0x081F, 0x0820, 0x0821, 0x0822,
	0x0823, 0x0825, 0x0826, 0x0827, 0x0829, 0x082A, 0x082B, 0x082C, 0x082D, 0x0951, 0x0953, 0x0954,
	0x0F82, 0x0F83, 0x0F86, 0x0F87, 0x135D, 0x135E, 0x135F, 0x17DD, 0x193A, 0x1A17, 0x1A75, 0x1A76,
	0x1A77, 0x1A78, 0x1A79, 0x1A7A, 0x1A7B, 0x1A7C, 0x1B6B, 0x1B6D, 0x1B6E, 0x1B6F, 0x1B70, 0x1B71,
	0x1B72, 0x1B73, 0x1CD0, 0x1CD1, 0x1CD2, 0x1CDA, 0x1CDB, 0x1CE0, 0x1DC0, 0x1DC1, 0x1DC3, 0x1DC4,
	0x1DC5, 0x1DC6, 0x1DC7, 0x1DC8, 0x1DC9, 0x1DCB, 0x1DCC, 0x1DD1, 0x1DD2, 0x1DD3, 0x1DD4, 0x1DD5,
	0x1DD6, 0x1DD7, 0x1DD8, 0x1DD9, 0x1DDA, 0x1DDB, 0x1DDC, 0x1DDD, 0x1DDE, 0x1DDF, 0x1DE0, 0x1DE1,
	0x1DE2, 0x1DE3, 0x1DE4, 0x1DE5, 0x1DE6, 0x1DFE, 0x20D0, 0x20D1, 0x20D4, 0x20D5, 0x20D6, 0x20D7,
	0x20DB, 0x20DC, 0x20E1, 0x20E7, 0x20E9, 0x20F0, 0x2CEF, 0x2CF0, 0x2CF1, 0x2DE0, 0x2DE1, 0x2DE2,
	0x2DE3, 0x2DE4, 0x2DE5, 0x2DE6, 0x2DE7, 0x2DE8, 0x2DE9, 0x2DEA, 0x2DEB, 0x2DEC, 0x2DED, 0x2DEE,
	0x2DEF, 0x2DF0, 0x2DF1, 0x2DF2, 0x2DF3, 0x2DF4, 0x2DF5, 0x2DF6, 0x2DF7, 0x2DF8, 0x2DF9, 0x2DFA,
	0x2DFB, 0x2DFC, 0x2DFD, 0x2DFE, 0x2DFF, 0xA66F, 0xA67C, 0xA67D, 0xA6F0, 0xA6F1, 0xA8E0, 0xA8E1,
	0xA8E2, 0xA8E3, 0xA8E4, 0xA8E5, 0xA8E6, 0xA8E7, 0xA8E8, 0xA8E9, 0xA8EA, 0xA8EB, 0xA8EC, 0xA8ED,
	0xA8EE, 0xA8EF, 0xA8F0, 0xA8F1, 0xAAB0, 0xAAB2, 0xAAB3, 0xAAB7, 0xAAB8, 0xAABE, 0xAABF, 0xAAC1,
	0xFE20, 0xFE21, 0xFE22, 0xFE23, 0xFE24, 0xFE25, 0xFE26, 0x10A0F, 0x10A38, 0x1D185, 0x1D186, 0x1D187,
	0x1D188, 0x1D189, 0x1D1AA, 0x1D1AB, 0x1D1AC, 0x1D1AD, 0x1D242, 0x1D243, 0x1D244,
}

// KittyImage encodes img for the Kitty graphics protocol's Unicode-placeholder
// mode, returning a zero-width `transmit` escape and `lines`, a cols×rows grid
// of placeholder cells to paint where the image should appear. The terminal
// loads the image under `id` as a virtual placement (a=T,U=1) sized cols×rows
// cells, then composites it onto the placeholder run. Embed `transmit` once on
// the first art line: it re-sends whenever that line is repainted (a cover or
// size change), and the placeholders alone suffice while the image is stable.
//
// Both the transmit escape (an APC) and each placeholder cell are width-correct
// to lipgloss/runewidth (0 and 1 respectively), so the result drops into the
// existing layout without disturbing column math. Returns ("", nil) on bad
// input. Caller must keep id within the diacritic-addressable range and the box
// within len(kittyDiacritics) cells. pxW/pxH are the placement's device-pixel
// footprint (cols·cellW × rows·cellH); pass 0 when the cell size is unknown to
// fall back to a longest-edge heuristic.
func KittyImage(img image.Image, cols, rows, id, pxW, pxH int) (transmit string, lines []string) {
	if img == nil || cols <= 0 || rows <= 0 || cols > len(kittyDiacritics) || rows > len(kittyDiacritics) {
		return "", nil
	}
	px := sizeForPlacement(img, pxW, pxH)
	var buf bytes.Buffer
	if err := png.Encode(&buf, px); err != nil {
		return "", nil
	}
	return kittyTransmit(id, cols, rows, base64.StdEncoding.EncodeToString(buf.Bytes())),
		kittyPlaceholders(id, cols, rows)
}

// sizeForPlacement renders img into the placement's device-pixel footprint
// (pxW×pxH) preserving the source's aspect ratio: the cover is scaled to fit and
// centred on a transparent canvas, so a non-square footprint never stretches it
// (the transparent margins read as the terminal background). With pxW/pxH ≤ 0
// (cell size unknown) it falls back to the longest-edge fit. The footprint is
// capped at kittyMaxPx per axis to bound the payload. Enlargement uses bilinear
// resampling (smooth); reduction uses box averaging — area averaging would alias
// when upscaling.
func sizeForPlacement(img image.Image, pxW, pxH int) image.Image {
	if pxW <= 0 || pxH <= 0 {
		return fit(img, kittyMinPx, kittyMaxPx)
	}
	if pxW > kittyMaxPx {
		pxW = kittyMaxPx
	}
	if pxH > kittyMaxPx {
		pxH = kittyMaxPx
	}
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return img
	}
	// scale to fit within the footprint, preserving aspect (contain)
	scale := math.Min(float64(pxW)/float64(sw), float64(pxH)/float64(sh))
	dw := int(float64(sw)*scale + 0.5)
	dh := int(float64(sh)*scale + 0.5)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	var scaled *image.RGBA
	if scale > 1 {
		scaled = resample(img, dw, dh) // bilinear: smooth enlargement
	} else {
		scaled = downscale(img, dw, dh) // box average: clean reduction
	}
	if dw >= pxW && dh >= pxH {
		return scaled // already fills the footprint, no margin needed
	}
	// centre the scaled cover on a transparent footprint-sized canvas
	canvas := image.NewRGBA(image.Rect(0, 0, pxW, pxH))
	ox, oy := (pxW-dw)/2, (pxH-dh)/2
	for y := 0; y < dh; y++ {
		di := canvas.PixOffset(ox, oy+y)
		si := scaled.PixOffset(0, y)
		copy(canvas.Pix[di:di+dw*4], scaled.Pix[si:si+dw*4])
	}
	return canvas
}

// fit scales img so its longest edge lands within [minPx, maxPx], preserving
// aspect. Used only as the fallback when the cell pixel size is unknown.
func fit(img image.Image, minPx, maxPx int) image.Image {
	b := img.Bounds()
	long := b.Dx()
	if b.Dy() > long {
		long = b.Dy()
	}
	target := long
	if target < minPx {
		target = minPx
	}
	if target > maxPx {
		target = maxPx
	}
	if long == 0 || target == long {
		return img
	}
	w, h := b.Dx()*target/long, b.Dy()*target/long
	// Match sizeForPlacement's direction split (and this function's own doc): a
	// sub-minPx cover is ENLARGED with bilinear resampling — box averaging would
	// degenerate to a blocky nearest-neighbour pick when upscaling.
	if target > long {
		return resample(img, w, h)
	}
	return downscale(img, w, h)
}

// resample scales src to w×h by bilinear interpolation — smooth for the
// enlargement a small cover needs to fill a large HiDPI placement, where
// downscale's box averaging would degenerate to a blocky nearest-neighbour pick.
func resample(src image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || sw <= 0 || sh <= 0 {
		return dst
	}
	clamp := func(v, hi int) int {
		if v < 0 {
			return 0
		}
		if v > hi {
			return hi
		}
		return v
	}
	for dy := 0; dy < h; dy++ {
		fy := (float64(dy)+0.5)*float64(sh)/float64(h) - 0.5 // sample at the dest pixel centre
		y0 := int(math.Floor(fy))
		ty := fy - float64(y0)
		y0c, y1c := clamp(y0, sh-1), clamp(y0+1, sh-1)
		for dx := 0; dx < w; dx++ {
			fx := (float64(dx)+0.5)*float64(sw)/float64(w) - 0.5
			x0 := int(math.Floor(fx))
			tx := fx - float64(x0)
			x0c, x1c := clamp(x0, sw-1), clamp(x0+1, sw-1)
			r00, g00, b00 := rgb16(src, b.Min.X+x0c, b.Min.Y+y0c)
			r10, g10, b10 := rgb16(src, b.Min.X+x1c, b.Min.Y+y0c)
			r01, g01, b01 := rgb16(src, b.Min.X+x0c, b.Min.Y+y1c)
			r11, g11, b11 := rgb16(src, b.Min.X+x1c, b.Min.Y+y1c)
			dst.Set(dx, dy, color.RGBA{
				R: bilerp8(r00, r10, r01, r11, tx, ty),
				G: bilerp8(g00, g10, g01, g11, tx, ty),
				B: bilerp8(b00, b10, b01, b11, tx, ty),
				A: 0xff,
			})
		}
	}
	return dst
}

// rgb16 returns a pixel's 16-bit colour channels as floats (alpha dropped).
func rgb16(img image.Image, x, y int) (float64, float64, float64) {
	r, g, b, _ := img.At(x, y).RGBA()
	return float64(r), float64(g), float64(b)
}

// bilerp8 bilinearly blends four 16-bit corner samples by (tx, ty) and returns an
// 8-bit channel.
func bilerp8(c00, c10, c01, c11, tx, ty float64) uint8 {
	top := c00*(1-tx) + c10*tx
	bot := c01*(1-tx) + c11*tx
	return uint8(int(top*(1-ty)+bot*ty) >> 8)
}

// kittyTransmit builds the (possibly chunked) APC escape that transmits the PNG
// payload and creates the virtual placement. Per the protocol, every control
// key rides only the first chunk; continuation chunks carry just m (1 = more).
func kittyTransmit(id, cols, rows int, payload string) string {
	const chunk = 4096
	var b strings.Builder
	first := true
	for {
		n := chunk
		if n > len(payload) {
			n = len(payload)
		}
		piece := payload[:n]
		payload = payload[n:]
		more := 0
		if len(payload) > 0 {
			more = 1
		}
		b.WriteString("\x1b_G")
		if first {
			// a=T transmit+place, U=1 virtual (for unicode placeholders), f=100 PNG,
			// c/r placement size in cells, q=2 suppress all responses.
			fmt.Fprintf(&b, "a=T,U=1,i=%d,f=100,c=%d,r=%d,q=2,m=%d", id, cols, rows, more)
			first = false
		} else {
			fmt.Fprintf(&b, "m=%d", more)
		}
		b.WriteByte(';')
		b.WriteString(piece)
		b.WriteString("\x1b\\")
		if more == 0 {
			return b.String()
		}
	}
}

// kittyPlaceholders renders the rows×cols grid of placeholder cells. The image
// id is carried in the foreground color; each cell appends its row then column
// diacritic so the terminal knows which part of the image to draw. The id rides
// the 24-bit RGB foreground (its three low bytes) rather than a 256-color index:
// the latter only addresses ids 0–255, and a cell's two diacritics already imply
// a zero most-significant byte, so this correctly references any id < 2²⁴.
func kittyPlaceholders(id, cols, rows int) []string {
	ph := string(rune(KittyPlaceholder))
	fg := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", (id>>16)&0xff, (id>>8)&0xff, id&0xff)
	lines := make([]string, rows)
	for r := 0; r < rows; r++ {
		var b strings.Builder
		b.WriteString(fg)
		rowDia := string(kittyDiacritics[r])
		for c := 0; c < cols; c++ {
			b.WriteString(ph)
			b.WriteString(rowDia)
			b.WriteString(string(kittyDiacritics[c]))
		}
		b.WriteString("\x1b[39m") // reset foreground so it doesn't bleed past the art
		lines[r] = b.String()
	}
	return lines
}
