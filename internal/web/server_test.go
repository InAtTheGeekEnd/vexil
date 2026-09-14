package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/brand"
	"github.com/InAtTheGeekEnd/vexil/internal/engine"
	"github.com/InAtTheGeekEnd/vexil/internal/notify"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
	assets "github.com/InAtTheGeekEnd/vexil/web"
)

const testPassword = "correct horse battery"

// seed is a monitor for seedServer, with two recent checks that passed or,
// when down is true, two that failed.
type seed struct {
	name         string
	down, public bool
}

// seedServer is a running server with the seeded monitors. It returns their
// ids in order. Every monitor points at a closed port, so a real check fails.
func seedServer(t *testing.T, seeds ...seed) (*Server, *store.Store, []int64) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	now := time.Now()
	var ids []int64
	for _, sd := range seeds {
		m := &store.Monitor{Name: sd.name, Type: store.TypeTCP, Target: "127.0.0.1:1", IntervalS: 900, Public: sd.public}
		if err := st.CreateMonitor(ctx, m); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
		for _, age := range []time.Duration{time.Minute, 2 * time.Minute} {
			c := store.Check{MonitorID: m.ID, At: now.Add(-age), OK: !sd.down, LatencyMS: 12}
			if sd.down {
				c.Error = "connection refused"
			}
			if err := st.InsertCheck(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
	}
	eng := engine.New(st, engine.Options{Log: log})
	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Stop)
	s, err := New(st, Options{Log: log, Engine: eng})
	if err != nil {
		t.Fatal(err)
	}
	return s, st, ids
}

func newTestServer(t *testing.T, opts Options) (*Server, *store.Store) {
	t.Helper()
	return newServer(t, opts, true)
}

// newIdleServer is newTestServer with an engine that is never started. No
// check can run in the background, so a page shows exactly the state the
// test put in the store and the engine.
func newIdleServer(t *testing.T, opts Options) (*Server, *store.Store) {
	t.Helper()
	return newServer(t, opts, false)
}

func newServer(t *testing.T, opts Options, start bool) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if opts.Log == nil {
		opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.Engine == nil {
		opts.Engine = engine.New(st, engine.Options{Log: opts.Log})
		if start {
			if err := opts.Engine.Start(context.Background()); err != nil {
				t.Fatalf("engine.Start: %v", err)
			}
			t.Cleanup(opts.Engine.Stop)
		}
	}
	if opts.Notifier == nil {
		opts.Notifier = notify.NewService(st, notify.Options{Log: opts.Log, Brand: brand.Default.Name})
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
	if err := st.SetPasswordHash(context.Background(), hash, ""); err != nil {
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
		noEngine   bool
		stopEngine bool
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
			name:       "no engine",
			noEngine:   true,
			wantStatus: http.StatusServiceUnavailable,
			want:       map[string]string{"status": "fail", "database": "ok", "engine": "not started"},
		},
		{
			name:       "engine stopped",
			stopEngine: true,
			wantStatus: http.StatusServiceUnavailable,
			want:       map[string]string{"status": "fail", "database": "ok", "engine": "stopped"},
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
			s, st := newTestServer(t, Options{})
			if tt.noEngine {
				s.engine = nil
			}
			if tt.stopEngine {
				s.engine.Stop()
			}
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

// TestAuthFormsRefuseLargeBodies posts bodies that are not a small
// URL-encoded form to the two forms that need no login. A multipart upload
// must be refused before its body is read, so no temp file is written.
func TestAuthFormsRefuseLargeBodies(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	// More than the 32 MB that ParseMultipartForm keeps in memory.
	const fileSize = 40 << 20
	upload := func() (io.Reader, string) {
		var head bytes.Buffer
		mw := multipart.NewWriter(&head)
		if err := mw.WriteField("password", testPassword); err != nil {
			t.Fatal(err)
		}
		if _, err := mw.CreateFormFile("file", "big.bin"); err != nil {
			t.Fatal(err)
		}
		tail := strings.NewReader("\r\n--" + mw.Boundary() + "--\r\n")
		return io.MultiReader(&head, io.LimitReader(zeros{}, fileSize), tail), mw.FormDataContentType()
	}
	bigForm := func() (io.Reader, string) {
		return strings.NewReader("password=" + strings.Repeat("a", 100<<10)), "application/x-www-form-urlencoded"
	}
	tests := []struct {
		name     string
		path     string
		body     func() (io.Reader, string)
		wantCode int
		wantRead bool // the handler may read the body
	}{
		{"multipart login", "/login", upload, http.StatusUnsupportedMediaType, false},
		{"multipart setup", "/setup", upload, http.StatusUnsupportedMediaType, false},
		{"large form login", "/login", bigForm, http.StatusRequestEntityTooLarge, true},
		{"large form setup", "/setup", bigForm, http.StatusRequestEntityTooLarge, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, st := newTestServer(t, Options{})
			if tt.path == "/login" {
				setPassword(t, st)
			}
			r, ctype := tt.body()
			body := &countingReader{r: r}
			req := httptest.NewRequest(http.MethodPost, tt.path, body)
			req.Header.Set("Content-Type", ctype)
			req.RemoteAddr = "203.0.113.7:1234"
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if !tt.wantRead && body.n > 0 {
				t.Errorf("the handler read %d bytes of the body, want none", body.n)
			}
			entries, err := os.ReadDir(tmp)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "multipart-") {
					t.Errorf("temp file %s was written", e.Name())
				}
			}
		})
	}
	// Setup still accepts its own form after the refused requests.
	s, _ := newTestServer(t, Options{})
	form := url.Values{"password": {testPassword}, "confirm": {testPassword}}
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("setup with a charset parameter = %d, want 303", rec.Code)
	}
}

// zeros reads as an endless run of zero bytes.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// countingReader counts the bytes read from r.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
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

// TestNoLiteralProductName enforces SPEC.md section 12: the product name
// comes from the brand setting. It scans the templates, the static JS and
// CSS, and every Go file outside internal/brand. Comments, the module
// path, the VEXIL_ environment names and the database file name are the
// only allowed places for the literal.
func TestNoLiteralProductName(t *testing.T) {
	needle := strings.ToLower(brand.Default.Name)
	allowed := []string{"github.com/", needle + "_", needle + ".db"}
	check := func(path string, b []byte) {
		for i, line := range strings.Split(string(b), "\n") {
			if idx := strings.Index(line, "//"); idx >= 0 {
				line = line[:idx]
			}
			l := strings.ToLower(line)
			if !strings.Contains(l, needle) {
				continue
			}
			ok := false
			for _, a := range allowed {
				if strings.Contains(l, a) {
					ok = true
				}
			}
			if !ok {
				t.Errorf("%s:%d contains the literal product name", path, i+1)
			}
		}
	}
	for _, root := range []string{"templates", "static"} {
		fsys := fs.FS(assets.Templates)
		if root == "static" {
			fsys = assets.Static
		}
		err := fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			ext := filepath.Ext(path)
			if root == "static" && ext != ".js" && ext != ".css" {
				return nil
			}
			b, err := fs.ReadFile(fsys, path)
			if err != nil {
				return err
			}
			check(path, b)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	repo := filepath.Join("..", "..")
	err := filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repo, path)
		if d.IsDir() {
			if rel == "." {
				return nil
			}
			if rel == filepath.Join("internal", "brand") || strings.HasPrefix(d.Name(), ".") || d.Name() == "data" {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		check(rel, b)
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
		{"/static/brand/logo.svg", "image/svg+xml"},
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

func TestPushEndpoint(t *testing.T) {
	s, st := newTestServer(t, Options{})
	m := &store.Monitor{Name: "Backup", Type: store.TypePush, IntervalS: 3600}
	if err := st.CreateMonitor(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	// The engine started before the monitor existed, so load it.
	if err := s.engine.Reload(context.Background(), m.ID); err != nil {
		t.Fatal(err)
	}
	events, stop := s.engine.Hub().Subscribe(8)
	defer stop()

	tests := []struct {
		method string
		token  string
		want   int
	}{
		{http.MethodGet, m.PushToken, http.StatusOK},
		{http.MethodPost, m.PushToken, http.StatusOK},
		{http.MethodGet, "wrong-token", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.token, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, httptest.NewRequest(tt.method, "/push/"+tt.token, nil))
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
	select {
	case ev := <-events:
		if ev.MonitorID != m.ID || ev.State != engine.Up {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event after push")
	}
}

func TestStaticVersioning(t *testing.T) {
	s, st := newTestServer(t, Options{})
	setPassword(t, st)
	if len(assetVersion) != 12 {
		t.Fatalf("assetVersion = %q, want 12 hex chars", assetVersion)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	page := rec.Body.String()
	for _, want := range []string{
		`href="/static/css/app.css?v=` + assetVersion + `"`,
		`src="/static/js/theme.js?v=` + assetVersion + `"`,
		`src="/static/js/app.js?v=` + assetVersion + `"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	tests := []struct {
		path string
		want string
	}{
		{"/static/css/app.css?v=" + assetVersion, "public, max-age=31536000, immutable"},
		{"/static/css/app.css?v=old", "no-cache"},
		{"/static/css/app.css", "no-cache"},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != tt.want {
			t.Errorf("%s: %d %q, want 200 %q", tt.path, rec.Code, rec.Header().Get("Cache-Control"), tt.want)
		}
	}
}

// TestSecureCookieBehindProxy checks that X-Forwarded-Proto counts only
// when the request comes from a loopback or private address.
func TestSecureCookieBehindProxy(t *testing.T) {
	s, st := newTestServer(t, Options{})
	setPassword(t, st)
	tests := []struct {
		name       string
		remoteAddr string
		proto      string
		wantSecure bool
	}{
		{"loopback proxy says https", "127.0.0.1:4321", "https", true},
		{"ipv6 loopback proxy says https", "[::1]:4321", "https", true},
		{"private proxy says https", "10.0.0.2:4321", "https", true},
		{"docker network proxy says https", "172.18.0.1:4321", "https", true},
		{"loopback proxy says http", "127.0.0.1:4321", "http", false},
		{"public client claims https", "203.0.113.7:4321", "https", false},
		{"no header", "127.0.0.1:4321", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			form := url.Values{"password": {testPassword}}
			req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.RemoteAddr = tt.remoteAddr
			if tt.proto != "" {
				req.Header.Set("X-Forwarded-Proto", tt.proto)
			}
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("login = %d, want 303", rec.Code)
			}
			var cookie *http.Cookie
			for _, ck := range rec.Result().Cookies() {
				if ck.Name == sessionCookie {
					cookie = ck
				}
			}
			if cookie == nil {
				t.Fatal("no session cookie set")
			}
			if cookie.Secure != tt.wantSecure {
				t.Fatalf("Secure = %v, want %v", cookie.Secure, tt.wantSecure)
			}
		})
	}
}
