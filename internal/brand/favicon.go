package brand

import (
	"encoding/base64"
	"regexp"
)

// The status colors of the tab icon: --up and --down of the light theme,
// with --bg of the light theme for the ring around the dot. One icon
// serves both themes.
const (
	UpColor   = "#16A34A"
	DownColor = "#DC2626"
	dotRing   = "#FAFAFA"
)

// bannerFill is the fill of the banner in logo.svg, which follows the
// accent color on the page.
var bannerFill = regexp.MustCompile(`fill="var\(--accent[^"]*\)"`)

// Favicon returns the tab icon for a brand in one state as an SVG. mark is
// the built-in logo.svg. The icons are SVG and not PNG because on a reload
// Safari replaces the icon of a page only with an SVG.
//
// The built-in logo gets its banner in green or red. An uploaded logo, SVG
// or PNG, is shown as it is, with a red dot in the bottom right corner when
// down.
func Favicon(b Brand, mark []byte, down bool) []byte {
	if !b.Logo.Empty() {
		svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">` +
			`<image href="data:` + b.Logo.Type + `;base64,` + base64.StdEncoding.EncodeToString(b.Logo.Data) + `" width="64" height="64"/>`
		if down {
			svg += `<circle cx="51.2" cy="51.2" r="12.8" fill="` + DownColor + `" stroke="` + dotRing + `" stroke-width="2"/>`
		}
		return []byte(svg + `</svg>`)
	}
	fill := UpColor
	if down {
		fill = DownColor
	}
	return bannerFill.ReplaceAll(mark, []byte(`fill="`+fill+`"`))
}
