package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/brand"
	"github.com/InAtTheGeekEnd/vexil/internal/engine"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// chromePath returns a headless-capable Chrome or Chromium binary, or "".
func chromePath() string {
	if p := os.Getenv("VEXIL_CHROME"); p != "" {
		return p
	}
	names := []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"}
	if runtime.GOOS == "darwin" {
		names = append([]string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Chromium.app/Contents/MacOS/Chromium"}, names...)
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

// faviconProbe is what the harness page reports about the dashboard tab.
type faviconProbe struct {
	Title  string   `json:"title"`
	Href   string   `json:"href"`
	Custom bool     `json:"custom"`
	Theme  string   `json:"theme"`
	Down   string   `json:"down"`
	Up     string   `json:"up"`
	Pixels [][4]int `json:"pixels"`
	Error  string   `json:"error"`
}

// faviconProbeJS runs on the dashboard. It draws the tab icon on a 64 by 64
// canvas and resolves with the pixels at the given points, plus the status
// colors of the page resolved to rgb().
const faviconProbeJS = `(function (points) {
  var link = document.querySelector("link[rel=icon]");
  var href = link ? link.getAttribute("href") : "";
  var root = document.documentElement;
  function resolve(v) {
    var el = document.createElement("span");
    el.style.color = getComputedStyle(root).getPropertyValue(v).trim();
    document.body.appendChild(el);
    var c = getComputedStyle(el).color;
    el.remove();
    return c;
  }
  var probe = { title: document.title, href: href.slice(0, 64), custom: !!(link && link.hasAttribute("data-custom")),
    theme: root.getAttribute("data-theme") || "", down: resolve("--down"), up: resolve("--up"), pixels: [], error: "" };
  return new Promise(function (done) {
    var img = new Image();
    img.onerror = function () { probe.error = "icon did not load"; done(probe); };
    img.onload = function () {
      var c = document.createElement("canvas");
      c.width = c.height = 64;
      var ctx = c.getContext("2d");
      var iw = img.naturalWidth || 64, ih = img.naturalHeight || 64;
      var s = Math.min(64 / iw, 64 / ih), w = iw * s, h = ih * s;
      ctx.drawImage(img, (64 - w) / 2, (64 - h) / 2, w, h);
      for (var i = 0; i < points.length; i++) {
        var d = ctx.getImageData(points[i][0], points[i][1], 1, 1).data;
        probe.pixels.push([d[0], d[1], d[2], d[3]]);
      }
      done(probe);
    };
    img.src = href;
  });
})`

// browserFixture is a logged-in server that headless Chrome can load
// without a cookie jar: the wrapper adds the admin session to each request.
func browserFixture(t *testing.T, s *Server, st *store.Store) (*httptest.Server, *http.Client) {
	t.Helper()
	setPassword(t, st)
	var cookies []*http.Cookie
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, ck := range cookies {
			r.AddCookie(ck)
		}
		s.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	c := client(t)
	if res := postForm(t, c, ts.URL+"/login", url.Values{"password": {testPassword}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("login = %d", res.StatusCode)
	}
	u, _ := url.Parse(ts.URL)
	cookies = c.Jar.Cookies(u)
	if len(cookies) == 0 {
		t.Fatal("login set no cookie")
	}
	return ts, c
}

// chrome is a headless Chrome driven over the DevTools protocol on a pipe.
// The protocol needs no WebSocket this way: each message is JSON followed
// by a NUL byte on file descriptors 3 and 4.
type chrome struct {
	t      *testing.T
	cmd    *exec.Cmd
	cancel context.CancelFunc
	w      *os.File
	r      *bufio.Reader
	id     int
}

type cdpMessage struct {
	ID     int             `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func startChrome(t *testing.T, path string) *chrome {
	t.Helper()
	toChrome, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	r, fromChrome, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	cmd := exec.CommandContext(ctx, path,
		"--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--user-data-dir="+filepath.Join(t.TempDir(), "profile"),
		"--remote-debugging-pipe", "about:blank")
	cmd.ExtraFiles = []*os.File{toChrome, fromChrome}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start chrome: %v", err)
	}
	toChrome.Close()
	fromChrome.Close()
	c := &chrome{t: t, cmd: cmd, cancel: cancel, w: w, r: bufio.NewReader(r)}
	t.Cleanup(func() {
		c.w.Close()
		cancel()
		cmd.Wait()
		r.Close()
	})
	return c
}

// call sends one command and returns its result. Events are skipped.
func (c *chrome) call(session, method string, params map[string]any) json.RawMessage {
	c.t.Helper()
	c.id++
	msg := map[string]any{"id": c.id, "method": method, "params": params}
	if session != "" {
		msg["sessionId"] = session
	}
	b, err := json.Marshal(msg)
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := c.w.Write(append(b, 0)); err != nil {
		c.t.Fatalf("write to chrome: %v", err)
	}
	for {
		line, err := c.r.ReadBytes(0)
		if err != nil {
			c.t.Fatalf("read from chrome during %s: %v", method, err)
		}
		var m cdpMessage
		if err := json.Unmarshal(line[:len(line)-1], &m); err != nil {
			c.t.Fatalf("chrome message: %v", err)
		}
		if m.Method != "" || m.ID != c.id {
			continue
		}
		if m.Error != nil {
			c.t.Fatalf("%s: %s", method, m.Error)
		}
		return m.Result
	}
}

// eval runs an expression on the page and returns its value, waiting for a
// promise to settle first.
func (c *chrome) eval(session, expr string, out any) {
	c.t.Helper()
	res := c.call(session, "Runtime.evaluate", map[string]any{"expression": expr, "awaitPromise": true, "returnByValue": true})
	var r struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails json.RawMessage `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		c.t.Fatal(err)
	}
	if r.ExceptionDetails != nil {
		c.t.Fatalf("page script failed: %s", r.ExceptionDetails)
	}
	if err := json.Unmarshal(r.Result.Value, out); err != nil {
		c.t.Fatalf("page value %s: %v", r.Result.Value, err)
	}
}

// probeFavicon opens the dashboard with the given theme in headless Chrome,
// waits for the tab icon to settle and reads its pixels at the points.
func probeFavicon(t *testing.T, path, base, theme string, points ...[2]int) faviconProbe {
	t.Helper()
	c := startChrome(t, path)
	var target struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(c.call("", "Target.createTarget", map[string]any{"url": "about:blank"}), &target); err != nil {
		t.Fatal(err)
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(c.call("", "Target.attachToTarget", map[string]any{"targetId": target.TargetID, "flatten": true}), &attached); err != nil {
		t.Fatal(err)
	}
	session := attached.SessionID
	c.call(session, "Page.enable", map[string]any{})
	navigate := func() {
		c.call(session, "Page.navigate", map[string]any{"url": base + "/"})
		for i := 0; ; i++ {
			var ready string
			c.eval(session, "document.readyState", &ready)
			if ready == "complete" {
				return
			}
			if i > 100 {
				t.Fatal("dashboard did not load")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	// settled loads the dashboard and returns the icon URL once it has
	// kept the same value for a while: it can change once more after load
	// when the uploaded logo finishes loading.
	settled := func() string {
		navigate()
		var last string
		for i, stable := 0, 0; stable < 3; i++ {
			var href string
			c.eval(session, "document.querySelector('link[rel=icon]').getAttribute('href')", &href)
			if href == last {
				stable++
			} else {
				stable, last = 0, href
			}
			if i > 100 {
				t.Fatal("the icon did not settle")
			}
			time.Sleep(100 * time.Millisecond)
		}
		return last
	}
	// The theme is chosen by the page script from localStorage, so it is
	// set on the origin first and the dashboard loaded again.
	navigate()
	var ok bool
	c.eval(session, "(localStorage.setItem('theme', "+strconv.Quote(theme)+"), true)", &ok)
	first := settled()
	// Safari keeps one icon per page URL and fetches only an icon URL it
	// has not cached, so the URL must differ on every load or the tab keeps
	// the color of the first visit.
	if second := settled(); second == first {
		t.Fatalf("the icon URL is the same on two loads: %.64s", first)
	}
	pts, _ := json.Marshal(points)
	var p faviconProbe
	c.eval(session, faviconProbeJS+"("+string(pts)+")", &p)
	if p.Error != "" {
		t.Fatalf("%s (href %q)", p.Error, p.Href)
	}
	return p
}

// rgb parses "rgb(1, 2, 3)" as reported by getComputedStyle.
func rgb(t *testing.T, s string) [3]int {
	t.Helper()
	s = strings.TrimSuffix(strings.TrimPrefix(s, "rgb("), ")")
	var out [3]int
	parts := strings.Split(s, ",")
	if len(parts) != 3 {
		t.Fatalf("not an rgb() color: %q", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			t.Fatalf("not an rgb() color: %q", s)
		}
		out[i] = n
	}
	return out
}

// near reports whether a pixel matches a color within a small tolerance.
func near(px [4]int, c [3]int) bool {
	for i := range c {
		d := px[i] - c[i]
		if d < -3 || d > 3 {
			return false
		}
	}
	return px[3] == 255
}

// downServer is a running server with one monitor up and, when down is
// true, one more that is down.
func downServer(t *testing.T, down bool) (*Server, *store.Store) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	now := time.Now()
	names := []string{"Alpha"}
	if down {
		names = append(names, "Bravo")
	}
	for i, name := range names {
		m := &store.Monitor{Name: name, Type: store.TypeTCP, Target: "127.0.0.1:1", IntervalS: 900}
		if err := st.CreateMonitor(ctx, m); err != nil {
			t.Fatal(err)
		}
		for _, age := range []time.Duration{time.Minute, 2 * time.Minute} {
			c := store.Check{MonitorID: m.ID, At: now.Add(-age), OK: i == 0, LatencyMS: 12}
			if i > 0 {
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
	return s, st
}

// TestFaviconPixels renders the dashboard in headless Chrome and reads the
// pixels of the tab icon. The built-in mark must carry the status color in
// its banner in both themes. An uploaded logo must get a red dot in the
// bottom right corner while a monitor is down and stay untouched while all
// are up.
func TestFaviconPixels(t *testing.T) {
	chrome := chromePath()
	if chrome == "" {
		t.Skip("no Chrome or Chromium found; set VEXIL_CHROME")
	}
	// The banner of the built-in mark fills x 89..423 and y 110..444 of a
	// 512 unit box: its centre is (32, 34) on a 64 pixel canvas. The dot on
	// an uploaded logo has its centre at 64 - 64/5.
	banner := [2]int{32, 34}
	dot := [2]int{51, 51}
	corner := [2]int{12, 12}

	t.Run("builtin", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			down  bool
			theme string
		}{
			{"down light", true, "light"},
			{"down dark", true, "dark"},
			{"up light", false, "light"},
			{"up dark", false, "dark"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s, st := downServer(t, tc.down)
				ts, _ := browserFixture(t, s, st)
				p := probeFavicon(t, chrome, ts.URL, tc.theme, banner)
				if p.Theme != tc.theme {
					t.Fatalf("page theme = %q, want %q", p.Theme, tc.theme)
				}
				wantTitle, want := "Dashboard · "+brand.Default.Name, rgb(t, p.Up)
				if tc.down {
					wantTitle, want = "(1) Dashboard · "+brand.Default.Name, rgb(t, p.Down)
				}
				if p.Title != wantTitle {
					t.Errorf("title = %q, want %q", p.Title, wantTitle)
				}
				if !strings.HasPrefix(p.Href, "data:image/svg+xml,") || p.Custom {
					t.Errorf("icon href = %q custom=%v, want an SVG data URL", p.Href, p.Custom)
				}
				if !near(p.Pixels[0], want) {
					t.Errorf("banner pixel = %v, want %v (--down %s, --up %s)", p.Pixels[0], want, p.Down, p.Up)
				}
			})
		}
	})

	t.Run("custom logo", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			down  bool
			theme string
		}{
			{"down light", true, "light"},
			{"down dark", true, "dark"},
			{"up light", false, "light"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s, st := downServer(t, tc.down)
				ts, c := browserFixture(t, s, st)
				logo := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><rect width="8" height="8" fill="#1E40AF"/></svg>`)
				if res := postMultipart(t, c, ts.URL+"/settings", map[string]string{"name": "Acme", "accent": "#4F46E5"}, logo); res.StatusCode != http.StatusOK {
					t.Fatalf("upload = %d", res.StatusCode)
				}
				logoURL := s.currentBrand().LogoURL
				p := probeFavicon(t, chrome, ts.URL, tc.theme, dot, corner)
				if !p.Custom {
					t.Fatalf("icon is not the uploaded logo: %q", p.Href)
				}
				blue := [3]int{0x1E, 0x40, 0xAF}
				if !near(p.Pixels[1], blue) {
					t.Errorf("logo pixel = %v, want %v", p.Pixels[1], blue)
				}
				if !tc.down {
					if p.Title != "Dashboard · Acme" {
						t.Errorf("title = %q", p.Title)
					}
					if !strings.HasPrefix(p.Href, logoURL+"#") {
						t.Errorf("icon href = %q, want the plain logo %q", p.Href, logoURL)
					}
					if !near(p.Pixels[0], blue) {
						t.Errorf("corner pixel = %v, want the plain logo %v", p.Pixels[0], blue)
					}
					return
				}
				if p.Title != "(1) Dashboard · Acme" {
					t.Errorf("title = %q, want %q", p.Title, "(1) Dashboard · Acme")
				}
				if !strings.HasPrefix(p.Href, "data:image/png") {
					t.Errorf("icon href = %q, want a PNG data URL", p.Href)
				}
				if want := rgb(t, p.Down); !near(p.Pixels[0], want) {
					t.Errorf("dot pixel = %v, want %v (--down %s)", p.Pixels[0], want, p.Down)
				}
			})
		}
	})
}
