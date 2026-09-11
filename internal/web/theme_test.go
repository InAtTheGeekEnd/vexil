package web

import (
	"regexp"
	"strings"
	"testing"

	"github.com/InAtTheGeekEnd/vexil/internal/brand"
	assets "github.com/InAtTheGeekEnd/vexil/web"
)

var tokenRE = regexp.MustCompile(`--([a-z-]+):(#[0-9A-F]{6})`)

// TestThemeCSSPassesAA parses the served tokens and checks the contrast of
// accent text on both page backgrounds and of button text on the accent.
func TestThemeCSSPassesAA(t *testing.T) {
	for _, accent := range []string{brand.Default.Accent, "#FACC15", "#0B0D10", "#FFFFFF", "#808080"} {
		css := themeCSS(accent)
		tokens := map[string]string{}
		for _, m := range tokenRE.FindAllStringSubmatch(css, -1) {
			tokens[m[1]] = m[2]
		}
		if tokens["accent"] != accent {
			t.Fatalf("%s: css = %q", accent, css)
		}
		if c := brand.Contrast(tokens["accent-text-light"], lightBackground); c < brand.AAText {
			t.Errorf("%s: light accent text %s has contrast %.2f", accent, tokens["accent-text-light"], c)
		}
		if c := brand.Contrast(tokens["accent-text-dark"], darkBackground); c < brand.AAText {
			t.Errorf("%s: dark accent text %s has contrast %.2f", accent, tokens["accent-text-dark"], c)
		}
		if c := brand.Contrast(tokens["on-accent"], accent); c < 3 {
			t.Errorf("%s: button text %s has contrast %.2f", accent, tokens["on-accent"], c)
		}
	}
}

// TestThemeBackgroundsMatchCSS keeps the Go constants equal to the tokens
// in app.css, so the contrast check uses the real page colors.
func TestThemeBackgroundsMatchCSS(t *testing.T) {
	css, err := assets.Static.ReadFile("static/css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	s := string(css)
	if !strings.Contains(s, "--bg: "+lightBackground+";") {
		t.Errorf("app.css light --bg is not %s", lightBackground)
	}
	if !strings.Contains(s, "--surface: "+darkBackground+";") {
		t.Errorf("app.css dark --surface is not %s", darkBackground)
	}
	if !strings.Contains(s, "--accent: "+brand.Default.Accent+";") {
		t.Errorf("app.css --accent is not the default %s", brand.Default.Accent)
	}
	for _, sel := range []string{"a {", ":focus-visible {", ":focus-visible::after {"} {
		for _, line := range strings.Split(s, "\n") {
			if strings.Contains(line, sel) && strings.Contains(line, "var(--accent)") {
				t.Errorf("accent used directly for text or a focus ring: %s", strings.TrimSpace(line))
			}
		}
	}
}
