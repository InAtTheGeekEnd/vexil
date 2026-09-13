package brand

import (
	"bytes"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"math"
)

// TouchIconSize is the side of the iOS home screen icon in pixels.
const TouchIconSize = 180

// TouchIcon draws the Apple touch icon for a brand as an opaque 180x180
// PNG. With a PNG logo it is that logo on white. Otherwise it is the
// built-in mark in white on the accent. An SVG logo cannot be drawn on the
// server without an SVG renderer, so it gets the built-in icon too.
func TouchIcon(b Brand) ([]byte, error) {
	img := image.NewNRGBA(image.Rect(0, 0, TouchIconSize, TouchIconSize))
	drawn := false
	if b.Logo.Type == "image/png" {
		if logo, err := png.Decode(bytes.NewReader(b.Logo.Data)); err == nil {
			drawLogo(img, logo)
			drawn = true
		}
	}
	if !drawn {
		drawMark(img, b.Accent)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// The mark from web/static/brand/logo.svg in its 512 unit viewBox. Every
// stroke is 28 units wide with round joins and caps, so a stroked
// rectangle becomes a rounded rectangle half a stroke larger and a
// stroked line becomes a capsule. Drawn in one color, the banner and its
// crossbar merge into one rectangle.
const (
	markHalfStroke = 14.0
	markFinialR    = 23.132 + markHalfStroke
	// The bounds of the drawn mark, strokes included.
	markMinX = 65.824 - markFinialR
	markMaxX = 446.176 + markFinialR
	markMinY = 10 - markHalfStroke
	markMaxY = 502 + markHalfStroke
	// The mark takes this share of the icon height.
	markShare = 0.6
)

var (
	markBanner  = box{88.955, 109.508, 423.044, 444.123}
	markFinials = []seg{{65.824, 110.097, 65.824, 152.329}, {446.176, 110.097, 446.176, 152.329}}
	markFeet    = []seg{{232.652, 502, 232.652, 444.123}, {232.652, 444.123, 279.347, 444.123}, {279.347, 444.123, 279.347, 502}}
	markSpike   = []pt{{256, 10}, {220.195, 64.121}, {241.387, 109.508}, {290.783, 109.508}, {311.805, 64.121}}

	markScale   = markShare * TouchIconSize / (markMaxY - markMinY)
	markOffsetX = (TouchIconSize-(markMaxX-markMinX)*markScale)/2 - markMinX*markScale
	markOffsetY = (TouchIconSize-(markMaxY-markMinY)*markScale)/2 - markMinY*markScale
)

type pt struct{ x, y float64 }

type box struct{ x0, y0, x1, y1 float64 }

type seg struct{ x0, y0, x1, y1 float64 }

// dist is the distance from a point to the box, 0 inside.
func (b box) dist(x, y float64) float64 {
	dx := math.Max(math.Max(b.x0-x, x-b.x1), 0)
	dy := math.Max(math.Max(b.y0-y, y-b.y1), 0)
	return math.Hypot(dx, dy)
}

// dist is the distance from a point to the segment.
func (s seg) dist(x, y float64) float64 {
	vx, vy := s.x1-s.x0, s.y1-s.y0
	t := 0.0
	if l := vx*vx + vy*vy; l > 0 {
		t = math.Max(0, math.Min(1, ((x-s.x0)*vx+(y-s.y0)*vy)/l))
	}
	return math.Hypot(x-(s.x0+t*vx), y-(s.y0+t*vy))
}

// inPolygon reports whether the point is inside the polygon (even-odd).
func inPolygon(p []pt, x, y float64) bool {
	in := false
	for i, j := 0, len(p)-1; i < len(p); j, i = i, i+1 {
		if (p[i].y > y) != (p[j].y > y) && x < (p[j].x-p[i].x)*(y-p[i].y)/(p[j].y-p[i].y)+p[i].x {
			in = !in
		}
	}
	return in
}

// inMark reports whether a point in viewBox units is on the drawn mark.
func inMark(x, y float64) bool {
	if markBanner.dist(x, y) <= markHalfStroke {
		return true
	}
	for _, s := range markFinials {
		if s.dist(x, y) <= markFinialR {
			return true
		}
	}
	for _, s := range markFeet {
		if s.dist(x, y) <= markHalfStroke {
			return true
		}
	}
	if inPolygon(markSpike, x, y) {
		return true
	}
	for i := range markSpike {
		j := (i + 1) % len(markSpike)
		if (seg{markSpike[i].x, markSpike[i].y, markSpike[j].x, markSpike[j].y}).dist(x, y) <= markHalfStroke {
			return true
		}
	}
	return false
}

// drawMark fills the image with the accent and draws the mark in white,
// with 4x4 samples per pixel for smooth edges.
func drawMark(dst *image.NRGBA, accent string) {
	bg := parseHex(accent)
	const n = 4
	for y := 0; y < TouchIconSize; y++ {
		for x := 0; x < TouchIconSize; x++ {
			hit := 0
			for sy := 0; sy < n; sy++ {
				for sx := 0; sx < n; sx++ {
					px := (float64(x) + (float64(sx)+0.5)/n - markOffsetX) / markScale
					py := (float64(y) + (float64(sy)+0.5)/n - markOffsetY) / markScale
					if inMark(px, py) {
						hit++
					}
				}
			}
			c := float64(hit) / (n * n)
			dst.SetNRGBA(x, y, color.NRGBA{toward(bg.R, c), toward(bg.G, c), toward(bg.B, c), 255})
		}
	}
}

// toward moves a channel toward white by the share c.
func toward(v uint8, c float64) uint8 {
	return uint8(math.Round(float64(v) + (255-float64(v))*c))
}

// parseHex reads a "#RRGGBB" accent, with the default accent for anything else.
func parseHex(accent string) color.NRGBA {
	if len(accent) != 7 || accent[0] != '#' {
		return color.NRGBA{R: 0x4F, G: 0x46, B: 0xE5, A: 255}
	}
	b, err := hex.DecodeString(accent[1:])
	if err != nil || len(b) != 3 {
		return color.NRGBA{R: 0x4F, G: 0x46, B: 0xE5, A: 255}
	}
	return color.NRGBA{R: b[0], G: b[1], B: b[2], A: 255}
}

// drawLogo fills the image with white and draws the logo in the middle,
// scaled to fit inside a margin. Each pixel averages a grid of samples
// from the logo, so a large logo shrinks without aliasing.
func drawLogo(dst *image.NRGBA, logo image.Image) {
	for i := range dst.Pix {
		dst.Pix[i] = 255
	}
	const margin = TouchIconSize / 10
	r := logo.Bounds()
	sw, sh := float64(r.Dx()), float64(r.Dy())
	if sw == 0 || sh == 0 {
		return
	}
	scale := math.Min((TouchIconSize-2*margin)/sw, (TouchIconSize-2*margin)/sh)
	w, h := sw*scale, sh*scale
	ox, oy := (TouchIconSize-w)/2, (TouchIconSize-h)/2
	n := int(math.Max(4, math.Min(16, math.Ceil(1/scale))))
	for y := int(oy); y < int(math.Ceil(oy+h)); y++ {
		for x := int(ox); x < int(math.Ceil(ox+w)); x++ {
			var sr, sg, sb, sa float64
			for sy := 0; sy < n; sy++ {
				for sx := 0; sx < n; sx++ {
					px := (float64(x) + (float64(sx)+0.5)/float64(n) - ox) / scale
					py := (float64(y) + (float64(sy)+0.5)/float64(n) - oy) / scale
					if px < 0 || py < 0 || px >= sw || py >= sh {
						continue
					}
					cr, cg, cb, ca := logo.At(r.Min.X+int(px), r.Min.Y+int(py)).RGBA()
					sr, sg, sb, sa = sr+float64(cr), sg+float64(cg), sb+float64(cb), sa+float64(ca)
				}
			}
			// The channels are premultiplied, so each sample over white is
			// c + (opaque - alpha). Average the samples, then scale to 8 bits.
			white := float64(n*n)*0xFFFF - sa
			out := func(c float64) uint8 { return uint8(math.Round((c + white) / float64(n*n) / 257)) }
			dst.SetNRGBA(x, y, color.NRGBA{out(sr), out(sg), out(sb), 255})
		}
	}
}
