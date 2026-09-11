package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/InAtTheGeekEnd/vexil/internal/brand"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
	assets "github.com/InAtTheGeekEnd/vexil/web"
)

const testPassword = "correct horse battery"

func newTestServer(t *testing.T, opts Options) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if opts.Log == nil {
		opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s, err := New(st, opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, st
}

// client returns an http.Client with a cookie jar that does not follow redirects.
func client(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func setPassword(t *testing.T, st *store.Store) {
	t.Helper()
	hash, err := HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPasswordHash(context.Background(), hash); err != nil {
		t.Fatal(err)
	}
}

func postForm(t *testing.T, c *http.Client, target string, form url.Values) *http.Response {
	t.Helper()
	res, err := c.PostForm(target, form)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func get(t *testing.T, c *http.Client, target string) *http.Response {
	t.Helper()
	res, err := c.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func body(t *testing.T, res *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestHealthz(t *testing.T) {
	s, _ := newTestServer(t, Options{})
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("got %d %q, want 200 ok", rec.Code, rec.Body.String())
	}
}

func TestReadyz(t *testing.T) {
	tests := []struct {
		name       string
		engine     func() error
		closeStore bool
		wantStatus int
		want       map[string]string
	}{
		{
			name:       "ready",
			wantStatus: http.StatusOK,
			want:       map[string]string{"status": "ok", "database": "ok", "engine": "ok"},
		},
		{
			name:       "engine down",
			engine:     func() error { return errors.New("not started") },
			wantStatus: http.StatusServiceUnavailable,
			want:       map[string]string{"status": "fail", "database": "ok", "engine": "not started"},
		},
		{
			name:       "database down",
			closeStore: true,
			wantStatus: http.StatusServiceUnavailable,
			want:       map[string]string{"status": "fail", "engine": "ok"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, st := newTestServer(t, Options{EngineReady: tt.engine})
			if tt.closeStore {
				st.Close()
			}
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			var got map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("bad JSON %q: %v", rec.Body.String(), err)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q", k, got[k], v)
				}
			}
			if tt.closeStore && got["database"] == "ok" {
				t.Errorf("database = ok, want a failure reason")
			}
		})
	}
}

func TestSecurityHeaders(t *testing.T) {
	s, _ := newTestServer(t, Options{})
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy"} {
		if rec.Header().Get(h) == "" {
			t.Errorf("missing header %s", h)
		}
	}
}

func TestFirstRunRedirects(t *testing.T) {
	s, _ := newTestServer(t, Options{})
	tests := []struct {
		path     string
		wantCode int
		wantLoc  string
	}{
		{"/", http.StatusFound, "/setup"},
		{"/login", http.StatusFound, "/setup"},
		{"/setup", http.StatusOK, ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.wantCode || rec.Header().Get("Location") != tt.wantLoc {
				t.Fatalf("got %d -> %q, want %d -> %q", rec.Code, rec.Header().Get("Location"), tt.wantCode, tt.wantLoc)
			}
		})
	}
}

func TestSetup(t *testing.T) {
	tests := []struct {
		name     string
		password string
		confirm  string
		wantCode int
		wantErr  string
	}{
		{"too short", "short", "short", http.StatusBadRequest, "at least 10 characters"},
		{"mismatch", "long enough one", "long enough two", http.StatusBadRequest, "do not match"},
		{"ok", testPassword, testPassword, http.StatusSeeOther, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newTestServer(t, Options{})
			ts := httptest.NewServer(s)
			defer ts.Close()
			c := client(t)

			res := postForm(t, c, ts.URL+"/setup", url.Values{"password": {tt.password}, "confirm": {tt.confirm}})
			if res.StatusCode != tt.wantCode {
				t.Fatalf("status = %d, want %d", res.StatusCode, tt.wantCode)
			}
			if tt.wantErr != "" {
				if b := body(t, res); !strings.Contains(b, tt.wantErr) {
					t.Fatalf("body does not contain %q", tt.wantErr)
				}
				return
			}
			if loc := res.Header.Get("Location"); loc != "/" {
				t.Fatalf("Location = %q, want /", loc)
			}
			// Setup logs the user in and the dashboard is visible.
			dash := get(t, c, ts.URL+"/")
			if dash.StatusCode != http.StatusOK || !strings.Contains(body(t, dash), "Add your first monitor") {
				t.Fatalf("dashboard after setup: status %d", dash.StatusCode)
			}
			// Setup is closed once a password exists.
			again := get(t, c, ts.URL+"/setup")
			if again.StatusCode != http.StatusFound || again.Header.Get("Location") != "/login" {
				t.Fatalf("second /setup: %d -> %q, want 302 -> /login", again.StatusCode, again.Header.Get("Location"))
			}
		})
	}
}

func TestLoginLogout(t *testing.T) {
	s, st := newTestServer(t, Options{})
	setPassword(t, st)
	ts := httptest.NewServer(s)
	defer ts.Close()
	c := client(t)

	// Anonymous visitors go to the login page.
	if res := get(t, c, ts.URL+"/"); res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/login" {
		t.Fatalf("anonymous /: %d -> %q", res.StatusCode, res.Header.Get("Location"))
	}
	if res := get(t, c, ts.URL+"/login"); res.StatusCode != http.StatusOK {
		t.Fatalf("GET /login = %d", res.StatusCode)
	} else if b := body(t, res); !strings.Contains(b, "reset-password") {
		t.Fatal("login page lacks the reset-password help line")
	}

	// Wrong password.
	if res := postForm(t, c, ts.URL+"/login", url.Values{"password": {"nope nope nope"}}); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d, want 401", res.StatusCode)
	}

	// Right password sets a session cookie.
	res := postForm(t, c, ts.URL+"/login", url.Values{"password": {testPassword}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/" {
		t.Fatalf("login = %d -> %q", res.StatusCode, res.Header.Get("Location"))
	}
	var cookie *http.Cookie
	for _, ck := range res.Cookies() {
		if ck.Name == sessionCookie {
			cookie = ck
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie set")
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Fatalf("cookie flags wrong: %+v", cookie)
	}

	if res := get(t, c, ts.URL+"/"); res.StatusCode != http.StatusOK {
		t.Fatalf("dashboard after login = %d", res.StatusCode)
	}
	// A logged-in user does not see the login page again.
	if res := get(t, c, ts.URL+"/login"); res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/" {
		t.Fatalf("GET /login while logged in: %d -> %q", res.StatusCode, res.Header.Get("Location"))
	}

	// Logout ends the session.
	if res := postForm(t, c, ts.URL+"/logout", nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout = %d", res.StatusCode)
	}
	if res := get(t, c, ts.URL+"/"); res.StatusCode != http.StatusFound {
		t.Fatalf("dashboard after logout = %d, want 302", res.StatusCode)
	}
}

func TestLoginRateLimit(t *testing.T) {
	s, st := newTestServer(t, Options{})
	setPassword(t, st)
	for i := 1; i <= 6; i++ {
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("password=wrong+wrong+wrong"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "203.0.113.7:1234"
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		want := http.StatusUnauthorized
		if i > 5 {
			want = http.StatusTooManyRequests
		}
		if rec.Code != want {
			t.Fatalf("attempt %d: status %d, want %d", i, rec.Code, want)
		}
	}
	// Another IP is not affected.
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("password=wrong+wrong+wrong"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "198.51.100.9:1234"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("other IP: status %d, want 401", rec.Code)
	}
}

func TestNotFoundPage(t *testing.T) {
	s, _ := newTestServer(t, Options{Brand: brand.Brand{Name: "Acme Watch"}})
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/no/such/page", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if b := rec.Body.String(); !strings.Contains(b, "Page not found") || !strings.Contains(b, "Acme Watch") {
		t.Fatalf("404 page missing title or brand name")
	}
}

func TestBrandNameComesFromSetting(t *testing.T) {
	s, _ := newTestServer(t, Options{Brand: brand.Brand{Name: "Acme Watch"}})
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))
	b := rec.Body.String()
	if !strings.Contains(b, "Acme Watch") {
		t.Fatal("setup page does not show the brand name")
	}
	if strings.Contains(strings.ToLower(b), strings.ToLower(brand.Default.Name)) {
		t.Fatal("setup page shows the default product name when a custom brand is set")
	}
}

// TestTemplatesHaveNoLiteralProductName enforces SPEC.md section 12.
func TestTemplatesHaveNoLiteralProductName(t *testing.T) {
	needle := strings.ToLower(brand.Default.Name)
	err := fs.WalkDir(assets.Templates, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(assets.Templates, path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(strings.ToLower(line), needle) {
				t.Errorf("%s:%d contains the literal product name", path, i+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStaticAssets(t *testing.T) {
	s, _ := newTestServer(t, Options{})
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/css/app.css", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("css: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestStyleguide(t *testing.T) {
	s, st := newTestServer(t, Options{})
	setPassword(t, st)
	ts := httptest.NewServer(s)
	defer ts.Close()
	c := client(t)

	if res := get(t, c, ts.URL+"/styleguide"); res.StatusCode != http.StatusFound {
		t.Fatalf("anonymous /styleguide = %d, want 302", res.StatusCode)
	}
	postForm(t, c, ts.URL+"/login", url.Values{"password": {testPassword}})
	res := get(t, c, ts.URL+"/styleguide")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("/styleguide = %d", res.StatusCode)
	}
	b := body(t, res)
	for _, want := range []string{
		`data-theme="light"`, `data-theme="dark"`, `class="theme-toggle"`,
		`dot-up`, `dot-down`, `dot-paused`, `dot-pending`,
		`class="uptime"`, `<svg class="chart"`, `<svg class="sparkline"`,
		`class="card stat"`, `class="empty"`, `btn-primary`, `class="field"`,
	} {
		if !strings.Contains(b, want) {
			t.Errorf("styleguide lacks %q", want)
		}
	}
	// The CSP allows no inline styles or handlers.
	if strings.Contains(b, " style=") || strings.Contains(b, " on") && strings.Contains(b, `="return`) {
		t.Error("styleguide uses inline style or handler attributes")
	}
}

func TestStaticFontAndLogo(t *testing.T) {
	s, _ := newTestServer(t, Options{})
	tests := []struct {
		path  string
		ctype string
	}{
		{"/static/fonts/InterVariable.woff2", "font/woff2"},
		{"/static/fonts/OFL.txt", "text/plain"},
		{"/static/img/logo.svg", "image/svg+xml"},
		{"/static/js/theme.js", "javascript"},
		{"/static/js/chart.js", "javascript"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), tt.ctype) {
				t.Fatalf("%s: %d %q", tt.path, rec.Code, rec.Header().Get("Content-Type"))
			}
		})
	}
}
