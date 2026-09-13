package brand

import (
	"encoding/base64"
	"encoding/xml"
	"io"
	"strings"
	"testing"

	assets "github.com/InAtTheGeekEnd/vexil/web"
)

// TestFavicon builds each tab icon from the real logo.svg and checks the
// parts that show the state: the banner fill of the mark and the dot on an
// uploaded logo. Every icon must be well-formed XML.
func TestFavicon(t *testing.T) {
	mark, err := assets.Static.ReadFile("static/brand/logo.svg")
	if err != nil {
		t.Fatal(err)
	}
	pngLogo := Logo{Data: []byte("\x89PNG\r\n\x1a\nfake"), Type: "image/png"}
	svgLogo := Logo{Data: []byte("<svg/>"), Type: "image/svg+xml"}
	pngImage := `<image href="data:image/png;base64,` + base64.StdEncoding.EncodeToString(pngLogo.Data) + `"`
	svgImage := `<image href="data:image/svg+xml;base64,` + base64.StdEncoding.EncodeToString(svgLogo.Data) + `"`
	dot := `<circle cx="51.2" cy="51.2" r="12.8" fill="` + DownColor + `"`
	tests := []struct {
		name      string
		brand     Brand
		down      bool
		want, not []string
	}{
		{"mark up", Default, false, []string{`fill="` + UpColor + `"`}, []string{"var(", DownColor}},
		{"mark down", Default, true, []string{`fill="` + DownColor + `"`}, []string{"var(", UpColor}},
		{"png logo up", Brand{Logo: pngLogo}, false, []string{pngImage}, []string{"<circle"}},
		{"png logo down", Brand{Logo: pngLogo}, true, []string{pngImage, dot}, nil},
		{"svg logo up", Brand{Logo: svgLogo}, false, []string{svgImage}, []string{"<circle", "var("}},
		{"svg logo down", Brand{Logo: svgLogo}, true, []string{svgImage, dot}, []string{"var("}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(Favicon(tt.brand, mark, tt.down))
			for _, s := range tt.want {
				if !strings.Contains(got, s) {
					t.Errorf("icon lacks %q", s)
				}
			}
			for _, s := range tt.not {
				if strings.Contains(got, s) {
					t.Errorf("icon has %q", s)
				}
			}
			d := xml.NewDecoder(strings.NewReader(got))
			for {
				if _, err := d.Token(); err == io.EOF {
					break
				} else if err != nil {
					t.Fatalf("icon is not well-formed: %v", err)
				}
			}
		})
	}
}
