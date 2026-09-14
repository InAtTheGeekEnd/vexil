// Package brand holds the product identity shown in the UI.
//
// This file is the only place where the default product name may appear as a
// literal. Templates and messages must read the name from a Brand value.
package brand

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"strings"
)

// Brand is the identity shown in page titles, the header and notifications.
type Brand struct {
	// Name is the product name.
	Name string
	// Accent is the accent color as a CSS hex value: "#RRGGBB".
	Accent string
	// PoweredBy controls the "Powered by" footer line.
	PoweredBy bool
	// Logo is the uploaded logo. An empty Logo means the built-in logo.
	Logo Logo
}

// ProductName is the name of the product and of its binary. Unlike the
// brand name, it does not change with the white label, so help that names
// the binary uses it.
const ProductName = "vexil"

// Default is the built-in brand.
var Default = Brand{
	Name:      ProductName,
	Accent:    "#4F46E5",
	PoweredBy: true,
}

// MaxNameLen limits the product name.
const MaxNameLen = 40

// Logo is an uploaded logo file.
type Logo struct {
	Data []byte
	Type string // "image/svg+xml" or "image/png"
}

// Empty reports whether no logo was uploaded.
func (l Logo) Empty() bool { return len(l.Data) == 0 }

// Version is a short hash of the file, for cache-busting URLs.
func (l Logo) Version() string {
	if l.Empty() {
		return ""
	}
	sum := sha256.Sum256(l.Data)
	return hex.EncodeToString(sum[:])[:12]
}

// MaxLogoSize is the largest logo file accepted, in bytes.
const MaxLogoSize = 512 * 1024

// Logo errors.
var (
	ErrLogoTooLarge   = errors.New("the logo must be 512 KB or smaller")
	ErrLogoType       = errors.New("the logo must be an SVG or PNG file")
	ErrLogoDimensions = errors.New("the logo must be 4096 by 4096 pixels or smaller")
	ErrLogoBroken     = errors.New("the PNG logo cannot be read: export it again and upload the new file")
)

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// ParseLogo checks the content of an uploaded file and returns it as a Logo.
// It looks at the bytes, not at the file name.
func ParseLogo(data []byte) (Logo, error) {
	if len(data) > MaxLogoSize {
		return Logo{}, ErrLogoTooLarge
	}
	if bytes.HasPrefix(data, pngMagic) {
		// Decode it now, so a file that the server cannot draw is refused
		// before it is saved.
		if _, err := decodePNG(data); err != nil {
			return Logo{}, err
		}
		return Logo{Data: data, Type: "image/png"}, nil
	}
	if isSVG(data) {
		return Logo{Data: data, Type: "image/svg+xml"}, nil
	}
	return Logo{}, ErrLogoType
}

// isSVG reports whether the first element of the document is <svg>.
func isSVG(data []byte) bool {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		if start, ok := tok.(xml.StartElement); ok {
			return start.Name.Local == "svg"
		}
	}
}

// ErrAccent describes an invalid accent color.
var ErrAccent = errors.New("enter a color as a hex code, for example #4F46E5")

// ParseAccent accepts "#RRGGBB", "RRGGBB", "#RGB" or "RGB" and returns the
// color as "#RRGGBB" in upper case.
func ParseAccent(s string) (string, error) {
	s = strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(s), "#"))
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return "", ErrAccent
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", ErrAccent
	}
	return "#" + s, nil
}

// Hover shades of the accent, as app.css derives them with color-mix.
const (
	HoverLight = 0.12 // 12% black in the light theme
	HoverDark  = 0.15 // 15% white in the dark theme
)

// TextOn returns "#000000" or "#FFFFFF" for text on the accent. It takes
// the color whose lowest WCAG contrast over the accent and its two hover
// shades is the higher one.
func TextOn(accent string) string {
	backgrounds := []string{accent, Mix(accent, "#000000", HoverLight), Mix(accent, "#FFFFFF", HoverDark)}
	worst := func(text string) float64 {
		c := math.Inf(1)
		for _, bg := range backgrounds {
			c = math.Min(c, Contrast(text, bg))
		}
		return c
	}
	if worst("#000000") > worst("#FFFFFF") {
		return "#000000"
	}
	return "#FFFFFF"
}

// Mix blends t of the color "toward" into "color" in sRGB, like CSS
// color-mix(in srgb, color (1-t), toward t). Both are "#RRGGBB".
func Mix(color, toward string, t float64) string {
	a, errA := hex.DecodeString(strings.TrimPrefix(color, "#"))
	b, errB := hex.DecodeString(strings.TrimPrefix(toward, "#"))
	if errA != nil || errB != nil || len(a) != 3 || len(b) != 3 {
		return color
	}
	out := make([]byte, 3)
	for i := range out {
		out[i] = byte(math.Round(float64(a[i])*(1-t) + float64(b[i])*t))
	}
	return "#" + strings.ToUpper(hex.EncodeToString(out))
}

// AAText is the WCAG AA contrast ratio for normal text.
const AAText = 4.5

// Readable returns the accent, or the accent mixed toward "toward" (black
// or white) in 5% steps until it has AA contrast on the background. It
// gives the accent text color for one theme.
func Readable(accent, background, toward string) string {
	for t := 0.0; t < 1; t += 0.05 {
		c := Mix(accent, toward, t)
		if Contrast(c, background) >= AAText {
			return c
		}
	}
	return toward
}

// Contrast returns the WCAG contrast ratio between two "#RRGGBB" colors.
func Contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func luminance(color string) float64 {
	rgb, err := hex.DecodeString(strings.TrimPrefix(color, "#"))
	if err != nil || len(rgb) != 3 {
		return 0
	}
	return 0.2126*channel(rgb[0]) + 0.7152*channel(rgb[1]) + 0.0722*channel(rgb[2])
}

// channel linearises one sRGB channel as WCAG 2 defines it.
func channel(c byte) float64 {
	v := float64(c) / 255
	if v <= 0.03928 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

// ReadLogo reads at most MaxLogoSize+1 bytes so a large upload fails fast.
func ReadLogo(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxLogoSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxLogoSize {
		return nil, ErrLogoTooLarge
	}
	return data, nil
}
