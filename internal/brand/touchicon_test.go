package brand

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func decodeIcon(t *testing.T, b Brand) *image.NRGBA {
	t.Helper()
	data, err := TouchIcon(b)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got := img.Bounds().Size(); got.X != TouchIconSize || got.Y != TouchIconSize {
		t.Fatalf("size = %v", got)
	}
	out := image.NewNRGBA(img.Bounds())
	for y := 0; y < TouchIconSize; y++ {
		for x := 0; x < TouchIconSize; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			if c.A != 255 {
				t.Fatalf("pixel %d,%d has alpha %d", x, y, c.A)
			}
			out.SetNRGBA(x, y, c)
		}
	}
	return out
}

func redPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 40, 20))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+3] = 255, 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestTouchIcon(t *testing.T) {
	white := color.NRGBA{255, 255, 255, 255}
	tests := []struct {
		name           string
		brand          Brand
		corner, center color.NRGBA
	}{
		{"default", Default, color.NRGBA{0x4F, 0x46, 0xE5, 255}, white},
		{"custom accent", Brand{Accent: "#0E9F6E"}, color.NRGBA{0x0E, 0x9F, 0x6E, 255}, white},
		{"png logo on white", Brand{Accent: "#0E9F6E", Logo: Logo{Data: redPNG(t), Type: "image/png"}}, white, color.NRGBA{255, 0, 0, 255}},
		{"svg logo falls back to the mark", Brand{Accent: "#0E9F6E", Logo: Logo{Data: []byte("<svg/>"), Type: "image/svg+xml"}}, color.NRGBA{0x0E, 0x9F, 0x6E, 255}, white},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img := decodeIcon(t, tt.brand)
			if got := img.NRGBAAt(2, 2); got != tt.corner {
				t.Errorf("corner = %v, want %v", got, tt.corner)
			}
			if got := img.NRGBAAt(TouchIconSize/2, TouchIconSize/2); got != tt.center {
				t.Errorf("center = %v, want %v", got, tt.center)
			}
		})
	}
}

func TestTouchIconLogoKeepsAspect(t *testing.T) {
	img := decodeIcon(t, Brand{Accent: "#0E9F6E", Logo: Logo{Data: redPNG(t), Type: "image/png"}})
	// A 2:1 logo fills the width inside the margin and half of that height.
	red := color.NRGBA{255, 0, 0, 255}
	white := color.NRGBA{255, 255, 255, 255}
	if got := img.NRGBAAt(20, 90); got != red {
		t.Errorf("left edge = %v, want red", got)
	}
	if got := img.NRGBAAt(90, 40); got != white {
		t.Errorf("above the logo = %v, want white", got)
	}
}
