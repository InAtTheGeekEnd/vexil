package web

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/InAtTheGeekEnd/vexil/internal/brand"
)

const testPNG = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"

// postMultipart posts the settings form with an optional logo file.
func postMultipart(t *testing.T, c *http.Client, target string, fields map[string]string, logo []byte) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if logo != nil {
		fw, err := mw.CreateFormFile("logo", "logo.bin")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(logo); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	res, err := c.Post(target, mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func httptestRecord(s *Server, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestSettingsBrand(t *testing.T) {
	s, st := newTestServer(t, Options{})
	ts, c := loggedIn(t, s, st)

	res := get(t, c, ts.URL+"/settings")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /settings = %d", res.StatusCode)
	}
	b := body(t, res)
	if !strings.Contains(b, `value="`+brand.Default.Name+`"`) || !strings.Contains(b, `value="#4F46E5"`) {
		t.Fatal("settings page does not show the defaults")
	}

	fields := map[string]string{"name": "Acme Watch", "accent": "#facc15", "powered_by": ""}
	res = postMultipart(t, c, ts.URL+"/settings", fields, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("save = %d: %s", res.StatusCode, body(t, res))
	}
	b = body(t, res)
	if !strings.Contains(b, "Settings saved.") || !strings.Contains(b, `value="#FACC15"`) {
		t.Fatalf("saved page: %s", b)
	}
	if strings.Contains(b, "Powered by Acme Watch") {
		t.Fatal("footer line still shown after it was turned off")
	}

	// The brand reaches every page, the theme sheet and the notifier.
	res = get(t, c, ts.URL+"/")
	if b := body(t, res); !strings.Contains(b, "<title>Dashboard · Acme Watch</title>") || !strings.Contains(b, `href="/brand/theme.css?v=FACC15"`) {
		t.Fatalf("dashboard after save: %s", b[:400])
	}
	res = get(t, c, ts.URL+"/brand/theme.css?v=FACC15")
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Fatalf("theme content type = %q", ct)
	}
	if b := body(t, res); b != ":root{--accent:#FACC15;--on-accent:#000000}\n" {
		t.Fatalf("theme = %q", b)
	}
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("theme cache = %q", cc)
	}
	if name, _ := s.store.GetSetting(context.Background(), "brand_name"); name != "Acme Watch" {
		t.Fatalf("stored name = %q", name)
	}

	// A restart reads the saved brand.
	s2, err := New(st, Options{Log: s.log})
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.currentBrand(); got.Name != "Acme Watch" || got.Accent != "#FACC15" || got.PoweredBy {
		t.Fatalf("brand after reload = %+v", got.Brand)
	}
}

func TestSettingsValidation(t *testing.T) {
	s, st := newTestServer(t, Options{})
	ts, c := loggedIn(t, s, st)
	tests := []struct {
		name   string
		fields map[string]string
		logo   []byte
		want   string
	}{
		{"empty name", map[string]string{"name": "  ", "accent": "#4F46E5"}, nil, "Enter a product name."},
		{"long name", map[string]string{"name": strings.Repeat("a", 41), "accent": "#4F46E5"}, nil, "Use at most 40 characters."},
		{"bad accent", map[string]string{"name": "Acme", "accent": "purple"}, nil, "Enter a color as a hex code"},
		{"html logo", map[string]string{"name": "Acme", "accent": "#4F46E5"}, []byte("<html><script>alert(1)</script></html>"), "The logo must be an SVG or PNG file."},
		{"big logo", map[string]string{"name": "Acme", "accent": "#4F46E5"}, append([]byte(testPNG), make([]byte, brand.MaxLogoSize)...), "The logo must be 512 KB or smaller."},
	}
	for _, tc := range tests {
		res := postMultipart(t, c, ts.URL+"/settings", tc.fields, tc.logo)
		b := body(t, res)
		if res.StatusCode != http.StatusBadRequest || !strings.Contains(b, tc.want) {
			t.Errorf("%s: status %d, want 400 with %q", tc.name, res.StatusCode, tc.want)
		}
	}
	if got := s.currentBrand(); got.Name != brand.Default.Name || !got.Logo.Empty() {
		t.Fatalf("brand changed by an invalid save: %+v", got.Brand)
	}
}

func TestSettingsLogo(t *testing.T) {
	s, st := newTestServer(t, Options{})
	ts, c := loggedIn(t, s, st)
	if res := get(t, c, ts.URL+"/brand/logo"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("logo before upload = %d", res.StatusCode)
	}

	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><script>alert(1)</script><rect width="10" height="10"/></svg>`)
	res := postMultipart(t, c, ts.URL+"/settings", map[string]string{"name": "Acme", "accent": "#4F46E5", "powered_by": "1"}, svg)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("upload = %d: %s", res.StatusCode, body(t, res))
	}
	b := body(t, res)
	logoURL := s.currentBrand().LogoURL
	if logoURL == "" || !strings.HasPrefix(logoURL, "/brand/logo?v=") {
		t.Fatalf("logo url = %q", logoURL)
	}
	// The logo appears only as an <img> and as the icon, never inline.
	if !strings.Contains(b, `<img class="brand-logo" src="`+logoURL+`"`) || !strings.Contains(b, `<link rel="icon" href="`+logoURL+`" data-custom>`) {
		t.Fatalf("page does not reference the logo through img and icon: %s", b[:600])
	}
	if strings.Contains(b, "alert(1)") {
		t.Fatal("the uploaded SVG was inlined into the page")
	}
	if strings.Contains(b, `<svg class="brand-logo"`) {
		t.Fatal("the built-in logo is still shown")
	}

	res = get(t, c, ts.URL+logoURL)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET logo = %d", res.StatusCode)
	}
	headers := map[string]string{
		"Content-Type":            "image/svg+xml",
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'",
		"X-Content-Type-Options":  "nosniff",
		"Cache-Control":           "public, max-age=31536000, immutable",
	}
	for k, want := range headers {
		if got := res.Header.Get(k); got != want {
			t.Errorf("logo header %s = %q, want %q", k, got, want)
		}
	}
	if got, _ := io.ReadAll(res.Body); !bytes.Equal(got, svg) {
		t.Fatal("logo bytes changed")
	}
	if res := get(t, c, ts.URL+"/brand/logo"); res.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("unversioned logo cache = %q", res.Header.Get("Cache-Control"))
	}

	// The public page shows it too.
	if b := body(t, get(t, c, ts.URL+"/status")); !strings.Contains(b, `<img class="brand-logo" src="`+logoURL+`"`) {
		t.Fatal("status page does not show the uploaded logo")
	}

	// A PNG replaces it. A save without a file keeps it.
	res = postMultipart(t, c, ts.URL+"/settings", map[string]string{"name": "Acme", "accent": "#4F46E5"}, []byte(testPNG))
	if res.StatusCode != http.StatusOK || s.currentBrand().Logo.Type != "image/png" {
		t.Fatalf("png upload = %d, type %q", res.StatusCode, s.currentBrand().Logo.Type)
	}
	res = postMultipart(t, c, ts.URL+"/settings", map[string]string{"name": "Acme 2", "accent": "#4F46E5"}, nil)
	if res.StatusCode != http.StatusOK || s.currentBrand().Logo.Type != "image/png" {
		t.Fatalf("save without file = %d, type %q", res.StatusCode, s.currentBrand().Logo.Type)
	}
	if res := get(t, c, ts.URL+s.currentBrand().LogoURL); res.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("png content type = %q", res.Header.Get("Content-Type"))
	}

	res = postForm(t, c, ts.URL+"/settings/logo/delete", url.Values{})
	if res.StatusCode != http.StatusOK || !s.currentBrand().Logo.Empty() {
		t.Fatalf("delete = %d, logo empty = %v", res.StatusCode, s.currentBrand().Logo.Empty())
	}
	if b := body(t, res); !strings.Contains(b, `<svg class="brand-logo"`) || strings.Contains(b, "/brand/logo?") {
		t.Fatal("built-in logo not back after delete")
	}
	if res := get(t, c, ts.URL+"/brand/logo"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("logo after delete = %d", res.StatusCode)
	}
}

func TestSettingsPasswordChange(t *testing.T) {
	s, st := newTestServer(t, Options{})
	ts, c := loggedIn(t, s, st)
	other := client(t)
	if res := postForm(t, other, ts.URL+"/login", url.Values{"password": {testPassword}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("second login = %d", res.StatusCode)
	}

	res := postForm(t, c, ts.URL+"/settings/password", url.Values{"password": {"short"}, "confirm": {"short"}})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body(t, res), "at least 10 characters") {
		t.Fatalf("short password = %d", res.StatusCode)
	}
	res = postForm(t, c, ts.URL+"/settings/password", url.Values{"password": {"a-new-password"}, "confirm": {"a-new-passwor"}})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body(t, res), "do not match") {
		t.Fatalf("mismatch = %d", res.StatusCode)
	}

	res = postForm(t, c, ts.URL+"/settings/password", url.Values{"password": {"a-new-password"}, "confirm": {"a-new-password"}})
	if res.StatusCode != http.StatusOK || !strings.Contains(body(t, res), "Password changed.") {
		t.Fatalf("change = %d", res.StatusCode)
	}
	// This session stays, the other one is gone.
	if res := get(t, c, ts.URL+"/settings"); res.StatusCode != http.StatusOK {
		t.Fatalf("own session after change = %d", res.StatusCode)
	}
	if res := get(t, other, ts.URL+"/settings"); res.StatusCode != http.StatusFound {
		t.Fatalf("other session after change = %d, want redirect to login", res.StatusCode)
	}
	fresh := client(t)
	if res := postForm(t, fresh, ts.URL+"/login", url.Values{"password": {testPassword}}); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old password still works: %d", res.StatusCode)
	}
	if res := postForm(t, fresh, ts.URL+"/login", url.Values{"password": {"a-new-password"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("new password login = %d", res.StatusCode)
	}
}

func TestSettingsNeedLogin(t *testing.T) {
	s, st := newTestServer(t, Options{})
	setPassword(t, st)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/settings"}, {http.MethodPost, "/settings"},
		{http.MethodPost, "/settings/password"}, {http.MethodPost, "/settings/logo/delete"},
	} {
		rec := httptestRecord(s, tc.method, tc.path)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
			t.Errorf("%s %s = %d -> %q, want redirect to /login", tc.method, tc.path, rec.Code, rec.Header().Get("Location"))
		}
	}
}
