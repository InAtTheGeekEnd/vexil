package web

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/InAtTheGeekEnd/vexil/internal/brand"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// brandState is the brand in use, kept in memory so a page render does not
// read the settings table. It is replaced as a whole when the user saves.
type brandState struct {
	brand.Brand
	// LogoURL is the versioned URL of the uploaded logo, "" for the built-in.
	LogoURL string
	// ThemeURL is the versioned URL of the accent style sheet.
	ThemeURL string
}

var brandKeys = []string{
	store.SettingBrandName, store.SettingBrandAccent, store.SettingBrandPoweredBy,
	store.SettingBrandLogo, store.SettingBrandLogoType,
}

// loadBrand reads the saved brand over the defaults from Options.
func (s *Server) loadBrand(ctx context.Context) error {
	values, err := s.store.Settings(ctx, brandKeys...)
	if err != nil {
		return err
	}
	b := s.defaults
	if v := strings.TrimSpace(values[store.SettingBrandName]); v != "" {
		b.Name = v
	}
	if v, err := brand.ParseAccent(values[store.SettingBrandAccent]); err == nil {
		b.Accent = v
	}
	if v, ok := values[store.SettingBrandPoweredBy]; ok {
		b.PoweredBy = v == "1"
	}
	if data, err := base64.StdEncoding.DecodeString(values[store.SettingBrandLogo]); err == nil && len(data) > 0 {
		b.Logo = brand.Logo{Data: data, Type: values[store.SettingBrandLogoType]}
	}
	s.setBrand(b)
	return nil
}

func (s *Server) setBrand(b brand.Brand) {
	st := &brandState{Brand: b, ThemeURL: "/brand/theme.css?v=" + strings.TrimPrefix(b.Accent, "#")}
	if !b.Logo.Empty() {
		st.LogoURL = "/brand/logo?v=" + b.Logo.Version()
	}
	s.brand.Store(st)
}

// currentBrand returns the brand in use.
func (s *Server) currentBrand() *brandState {
	return s.brand.Load()
}

// saveBrand writes the name, accent and footer flag and reloads the state.
func (s *Server) saveBrand(ctx context.Context, b brand.Brand) error {
	powered := "0"
	if b.PoweredBy {
		powered = "1"
	}
	values := map[string]string{
		store.SettingBrandName:      b.Name,
		store.SettingBrandAccent:    b.Accent,
		store.SettingBrandPoweredBy: powered,
	}
	if !b.Logo.Empty() {
		values[store.SettingBrandLogo] = base64.StdEncoding.EncodeToString(b.Logo.Data)
		values[store.SettingBrandLogoType] = b.Logo.Type
	}
	if err := s.store.SetSettings(ctx, values); err != nil {
		return err
	}
	return s.loadBrand(ctx)
}

// deleteLogo removes the uploaded logo and reloads the state.
func (s *Server) deleteLogo(ctx context.Context) error {
	if err := s.store.DeleteSettings(ctx, store.SettingBrandLogo, store.SettingBrandLogoType); err != nil {
		return err
	}
	return s.loadBrand(ctx)
}

// handleLogo serves the uploaded logo. It is only ever shown through an
// <img> tag, and the policy header stops a browser from running anything
// inside an SVG that is opened on its own.
func (s *Server) handleLogo(w http.ResponseWriter, r *http.Request) {
	b := s.currentBrand()
	if b.Logo.Empty() {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", b.Logo.Type)
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Disposition", "inline")
	if r.URL.Query().Get("v") == b.Logo.Version() {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	_, _ = w.Write(b.Logo.Data)
}

// The page backgrounds that accent text sits on, from the tokens in
// app.css. Light text has the least contrast on the lighter dark surface,
// and dark text on the darker light page, so these are the worst cases.
const (
	lightBackground = "#FAFAFA" // --bg in the light theme
	darkBackground  = "#12151A" // --surface in the dark theme
)

// themeCSS returns the accent tokens for one accent: the accent, the text
// color on it, and an accent text color per theme that has AA contrast.
func themeCSS(accent string) string {
	return ":root{--accent:" + accent +
		";--on-accent:" + brand.TextOn(accent) +
		";--accent-text-light:" + brand.Readable(accent, lightBackground, "#000000") +
		";--accent-text-dark:" + brand.Readable(accent, darkBackground, "#FFFFFF") + "}\n"
}

// handleTheme serves the accent tokens as a tiny style sheet. Inline
// styles are blocked by the page policy, so they come from this URL.
func (s *Server) handleTheme(w http.ResponseWriter, r *http.Request) {
	b := s.currentBrand()
	h := w.Header()
	h.Set("Content-Type", "text/css; charset=utf-8")
	if r.URL.Query().Get("v") == strings.TrimPrefix(b.Accent, "#") {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	_, _ = w.Write([]byte(themeCSS(b.Accent)))
}
