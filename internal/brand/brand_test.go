package brand

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/png"
	"runtime"
	"strings"
	"testing"
)

func TestParseAccent(t *testing.T) {
	tests := []struct {
		in, want string
		wantErr  bool
	}{
		{"#4F46E5", "#4F46E5", false},
		{"4f46e5", "#4F46E5", false},
		{" #abc ", "#AABBCC", false},
		{"abc", "#AABBCC", false},
		{"#GGGGGG", "", true},
		{"#12345", "", true},
		{"", "", true},
		{"red", "", true},
	}
	for _, tc := range tests {
		got, err := ParseAccent(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("ParseAccent(%q) = %q, %v; want %q, err=%v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestTextOnPassesAA(t *testing.T) {
	tests := []struct{ bg, want string }{
		{"#FFFFFF", "#000000"},
		{"#000000", "#FFFFFF"},
		{"#FACC15", "#000000"}, // yellow
		{"#1D4ED8", "#FFFFFF"}, // blue
		{"#4F46E5", "#FFFFFF"},
	}
	for _, tc := range tests {
		got := TextOn(tc.bg)
		if got != tc.want {
			t.Errorf("TextOn(%s) = %s, want %s", tc.bg, got, tc.want)
		}
		if c := Contrast(tc.bg, got); c < 4.5 {
			t.Errorf("TextOn(%s) contrast %.2f is below AA", tc.bg, c)
		}
	}
}

func TestMix(t *testing.T) {
	tests := []struct {
		color, toward string
		t             float64
		want          string
	}{
		{"#4F46E5", "#000000", 0, "#4F46E5"},
		{"#4F46E5", "#FFFFFF", 1, "#FFFFFF"},
		{"#000000", "#FFFFFF", 0.5, "#808080"},
		{"#4F46E5", "#000000", HoverLight, "#463ECA"},
		{"bad", "#FFFFFF", 0.5, "bad"},
	}
	for _, tc := range tests {
		if got := Mix(tc.color, tc.toward, tc.t); got != tc.want {
			t.Errorf("Mix(%s, %s, %.2f) = %s, want %s", tc.color, tc.toward, tc.t, got, tc.want)
		}
	}
}

// TestReadableHasAAContrast checks accent text on the page backgrounds
// from app.css for a range of accents, including the default.
func TestReadableHasAAContrast(t *testing.T) {
	const light, dark = "#FAFAFA", "#12151A"
	accents := []string{Default.Accent, "#6366F1", "#FACC15", "#000000", "#FFFFFF", "#808080", "#16A34A", "#DC2626", "#0EA5E9"}
	for _, accent := range accents {
		if got := Readable(accent, light, "#000000"); Contrast(got, light) < AAText {
			t.Errorf("Readable(%s, light) = %s, contrast %.2f", accent, got, Contrast(got, light))
		}
		if got := Readable(accent, dark, "#FFFFFF"); Contrast(got, dark) < AAText {
			t.Errorf("Readable(%s, dark) = %s, contrast %.2f", accent, got, Contrast(got, dark))
		}
		text := TextOn(accent)
		for _, bg := range []string{accent, Mix(accent, "#000000", HoverLight), Mix(accent, "#FFFFFF", HoverDark)} {
			if c := Contrast(text, bg); c < 3 {
				t.Errorf("TextOn(%s) = %s has contrast %.2f on %s", accent, text, c, bg)
			}
		}
	}
	// The default accent passes as it is in the light theme and gets a
	// lighter tint in the dark theme.
	if got := Readable(Default.Accent, light, "#000000"); got != Default.Accent {
		t.Errorf("light accent text = %s, want the accent itself", got)
	}
	if got := Readable(Default.Accent, dark, "#FFFFFF"); got == Default.Accent {
		t.Error("dark accent text is the accent itself, which fails AA on the dark surface")
	}
	for _, bg := range []string{Default.Accent, Mix(Default.Accent, "#000000", HoverLight), Mix(Default.Accent, "#FFFFFF", HoverDark)} {
		if c := Contrast(TextOn(Default.Accent), bg); c < AAText {
			t.Errorf("default button text has contrast %.2f on %s", c, bg)
		}
	}
}

// pngWithHeader returns a PNG file whose header claims a w by h RGBA image.
// It has no pixel data. Built with w and h of 20000, it is 65 bytes.
func pngWithHeader(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	chunk := func(typ string, data []byte) {
		_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
		b.WriteString(typ)
		b.Write(data)
		crc := crc32.NewIEEE()
		crc.Write([]byte(typ))
		crc.Write(data)
		_ = binary.Write(&b, binary.BigEndian, crc.Sum32())
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], w)
	binary.BigEndian.PutUint32(ihdr[4:8], h)
	ihdr[8], ihdr[9] = 8, 6 // 8-bit RGBA
	chunk("IHDR", ihdr)
	var z bytes.Buffer
	_ = zlib.NewWriter(&z).Close()
	chunk("IDAT", z.Bytes())
	chunk("IEND", nil)
	return b.Bytes()
}

// validPNG is a real 2 by 2 PNG.
func validPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestParseLogo(t *testing.T) {
	svg := []byte(`<?xml version="1.0"?><!-- c --><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"/>`)
	if n := len(pngWithHeader(20000, 20000)); n != 65 {
		t.Fatalf("the bomb is %d bytes, want 65", n)
	}
	tests := []struct {
		name string
		data []byte
		typ  string
		err  error
	}{
		{"png", validPNG(t), "image/png", nil},
		{"svg", svg, "image/svg+xml", nil},
		{"65-byte png that claims 20000 by 20000", pngWithHeader(20000, 20000), "", ErrLogoDimensions},
		{"png that claims 2^24 by 2^24", pngWithHeader(1<<24, 1<<24), "", ErrLogoDimensions},
		{"png one pixel too wide", pngWithHeader(MaxLogoSide+1, 1), "", ErrLogoDimensions},
		{"png one pixel too tall", pngWithHeader(1, MaxLogoSide+1), "", ErrLogoDimensions},
		{"png with no pixel data", pngWithHeader(8, 8), "", ErrLogoBroken},
		{"png signature only", append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 20)...), "", ErrLogoBroken},
		{"html", []byte("<html><body>hi</body></html>"), "", ErrLogoType},
		{"text", []byte("hello"), "", ErrLogoType},
		{"empty", nil, "", ErrLogoType},
		{"large", []byte(strings.Repeat("<svg>", MaxLogoSize/5+1)), "", ErrLogoTooLarge},
	}
	for _, tc := range tests {
		l, err := ParseLogo(tc.data)
		if !errors.Is(err, tc.err) || l.Type != tc.typ {
			t.Errorf("%s: type %q err %v; want %q %v", tc.name, l.Type, err, tc.typ, tc.err)
		}
		if err == nil && l.Version() == "" {
			t.Errorf("%s: empty version", tc.name)
		}
	}
}

// TestTouchIconRefusesBombs draws the touch icon for a stored PNG logo whose
// header claims a huge image, as a database from before the upload check
// can hold. It must not panic or allocate the image, and it draws the
// built-in mark.
func TestTouchIconRefusesBombs(t *testing.T) {
	for _, side := range []uint32{20000, 1 << 24} {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		icon, err := TouchIcon(Brand{Accent: "#4F46E5", Logo: Logo{Data: pngWithHeader(side, side), Type: "image/png"}})
		runtime.ReadMemStats(&after)
		if err != nil || len(icon) == 0 {
			t.Fatalf("side %d: icon of %d bytes, err %v", side, len(icon), err)
		}
		if grew := after.TotalAlloc - before.TotalAlloc; grew > 64<<20 {
			t.Fatalf("side %d: TouchIcon allocated %d MB", side, grew>>20)
		}
	}
}
