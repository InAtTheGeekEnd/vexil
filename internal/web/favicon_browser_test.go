//go:build chrome

package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/brand"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

const (
	// browserWait is how long a Chrome test waits for one condition or for
	// one answer from Chrome. It is long so that a slow or busy machine
	// still passes. A wait ends as soon as its condition holds.
	browserWait = time.Minute
	// pollEvery is the time between two looks at a condition.
	pollEvery = 100 * time.Millisecond
)

// chromeMu lets one Chrome test run at a time: each one starts browsers and
// needs the machine to itself.
var chromeMu sync.Mutex

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

// requireChrome returns the browser for a Chrome test and holds the test
// until no other Chrome test runs. The Chrome tests build only with the
// chrome tag, so a missing browser fails the test.
func requireChrome(t *testing.T) string {
	t.Helper()
	path := chromePath()
	if path == "" {
		t.Fatal("no Chrome or Chromium found; set VEXIL_CHROME")
	}
	chromeMu.Lock()
	t.Cleanup(chromeMu.Unlock)
	return path
}

// poll looks at a condition every pollEvery until it holds. check reports
// whether it holds and what it saw. After browserWait the test fails with
// what it waited for and what it saw last.
func poll(t *testing.T, what string, check func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(browserWait)
	for {
		ok, saw := check()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited %v for %s, saw %q", browserWait, what, saw)
		}
		time.Sleep(pollEvery)
	}
}

// iconProbe is what the page reports about its tab icon.
type iconProbe struct {
	Title  string   `json:"title"`
	Href   string   `json:"href"`
	Theme  string   `json:"theme"`
	Pixels [][4]int `json:"pixels"`
	Error  string   `json:"error"`
}

// iconProbeJS runs on the page. It draws the tab icon on a 64 by 64 canvas
// and resolves with the pixels at the given points.
const iconProbeJS = `(function (points) {
  var link = document.querySelector("link[rel=icon]");
  var href = link ? link.getAttribute("href") : "";
  var probe = { title: document.title, href: href, theme: document.documentElement.getAttribute("data-theme") || "", pixels: [], error: "" };
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

// iconHref is a page expression for the href of the tab icon.
const iconHref = "document.querySelector('link[rel=icon]').getAttribute('href')"

// liveOpen runs before the page scripts and sets window.probeLive when the
// event stream of the page is open. The server subscribes before it answers,
// so an engine event after that reaches the page. An event before it is lost.
const liveOpen = `window.EventSource = class extends window.EventSource {
  constructor(url, init) {
    super(url, init);
    this.addEventListener("open", function () { window.probeLive = true; });
  }
};`

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
	t    *testing.T
	w    *os.File
	pipe *os.File // the end Chrome writes to, for read deadlines
	r    *bufio.Reader
	id   int
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
	// The profile is not a t.TempDir: Chrome's helper processes can write
	// to it for a moment after the browser exits, and that must not fail
	// the test when the directory is removed.
	profile, err := os.MkdirTemp("", "chrome-profile-")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path,
		"--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--user-data-dir="+profile, "--remote-debugging-pipe", "about:blank")
	cmd.ExtraFiles = []*os.File{toChrome, fromChrome}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start chrome: %v", err)
	}
	toChrome.Close()
	fromChrome.Close()
	c := &chrome{t: t, w: w, pipe: r, r: bufio.NewReader(r)}
	t.Cleanup(func() {
		// A clean close lets Chrome end its helper processes before the
		// profile is removed. Reading on keeps the pipe from filling up.
		_ = r.SetReadDeadline(time.Time{})
		go func() { _, _ = io.Copy(io.Discard, c.r) }()
		_, _ = c.send("", "Browser.close", map[string]any{})
		exited := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(exited)
		}()
		select {
		case <-exited:
		case <-time.After(browserWait):
			t.Logf("waited %v for Chrome to close, saw it still running; killed it", browserWait)
			_ = cmd.Process.Kill()
			<-exited
		}
		w.Close()
		r.Close()
		deadline := time.Now().Add(browserWait)
		for err := os.RemoveAll(profile); err != nil; err = os.RemoveAll(profile) {
			if time.Now().After(deadline) {
				t.Logf("waited %v to remove the Chrome profile, saw %v", browserWait, err)
				break
			}
			time.Sleep(pollEvery)
		}
	})
	return c
}

// send writes one command and returns its id.
func (c *chrome) send(session, method string, params map[string]any) (int, error) {
	c.id++
	msg := map[string]any{"id": c.id, "method": method, "params": params}
	if session != "" {
		msg["sessionId"] = session
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return 0, err
	}
	_, err = c.w.Write(append(b, 0))
	return c.id, err
}

// call sends one command and returns its result. Events are skipped.
func (c *chrome) call(session, method string, params map[string]any) json.RawMessage {
	c.t.Helper()
	res, err := c.try(session, method, params)
	if err != nil {
		c.t.Fatal(err)
	}
	return res
}

// try is call that returns an error answer from Chrome. It still fails the
// test when Chrome is gone or does not answer within browserWait.
func (c *chrome) try(session, method string, params map[string]any) (json.RawMessage, error) {
	c.t.Helper()
	id, err := c.send(session, method, params)
	if err != nil {
		c.t.Fatalf("send %s: %v", method, err)
	}
	if err := c.pipe.SetReadDeadline(time.Now().Add(browserWait)); err != nil {
		c.t.Fatal(err)
	}
	for {
		line, err := c.r.ReadBytes(0)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			c.t.Fatalf("waited %v for Chrome to answer %s, saw no answer", browserWait, method)
		}
		if err != nil {
			c.t.Fatalf("read from chrome during %s: %v", method, err)
		}
		var m cdpMessage
		if err := json.Unmarshal(line[:len(line)-1], &m); err != nil {
			c.t.Fatalf("chrome message: %v", err)
		}
		if m.Method != "" || m.ID != id {
			continue
		}
		if m.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, m.Error)
		}
		return m.Result, nil
	}
}

// eval runs an expression on the page and returns its value, waiting for a
// promise to settle first.
func (c *chrome) eval(session, expr string, out any) {
	c.t.Helper()
	if err := c.tryEval(session, expr, out); err != nil {
		c.t.Fatal(err)
	}
}

// tryEval is eval that returns an error when Chrome or the page script fails.
func (c *chrome) tryEval(session, expr string, out any) error {
	c.t.Helper()
	res, err := c.try(session, "Runtime.evaluate", map[string]any{"expression": expr, "awaitPromise": true, "returnByValue": true})
	if err != nil {
		return err
	}
	var r struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return err
	}
	if e := r.ExceptionDetails; e != nil {
		desc, _, _ := strings.Cut(e.Exception.Description, "\n")
		return fmt.Errorf("page script failed: %s %s", e.Text, desc)
	}
	if err := json.Unmarshal(r.Result.Value, out); err != nil {
		return fmt.Errorf("page value %s: %v", r.Result.Value, err)
	}
	return nil
}

// waitFor polls a page expression until it is true. saw is a page expression
// for the failure message. A look that fails, as it can while one document
// replaces another, counts as not yet.
func waitFor(t *testing.T, c *chrome, session, expr, saw, what string) {
	t.Helper()
	poll(t, what, func() (bool, string) {
		var r struct {
			OK  bool   `json:"ok"`
			Saw string `json:"saw"`
		}
		if err := c.tryEval(session, "({ok: !!("+expr+"), saw: String("+saw+")})", &r); err != nil {
			return false, err.Error()
		}
		return r.OK, r.Saw
	})
}

// waitForValue polls a page expression until its value as a string is want.
func waitForValue(t *testing.T, c *chrome, session, expr, want, what string) {
	t.Helper()
	waitFor(t, c, session, "String("+expr+") === "+strconv.Quote(want), expr, what+" "+strconv.Quote(want))
}

// waitForIcon polls until the tab icon links href with a fragment.
func waitForIcon(t *testing.T, c *chrome, session, href, what string) {
	t.Helper()
	waitFor(t, c, session, iconHref+".indexOf("+strconv.Quote(href+"#")+") === 0", iconHref, what)
}

// waitLive polls until the event stream of the page is open. The page must
// be opened with liveOpen.
func waitLive(t *testing.T, c *chrome, session string) {
	t.Helper()
	waitFor(t, c, session, "window.probeLive === true", "window.probeLive === true ? 'open' : 'not open'", "the event stream to open")
}

// loadPage navigates the page and waits for the new document. The marker is
// gone once the new document has replaced the old one.
func loadPage(t *testing.T, c *chrome, session, pageURL string) {
	t.Helper()
	var ok bool
	c.eval(session, "(window.probeOld = true)", &ok)
	c.call(session, "Page.navigate", map[string]any{"url": pageURL})
	waitFor(t, c, session, "document.readyState === 'complete' && !window.probeOld",
		"document.readyState + (window.probeOld ? ', old document' : ', new document')", pageURL+" to load")
}

// openPage opens a page with a theme in headless Chrome and returns the
// page session. init, when set, runs before the page scripts on every load.
// It loads the page twice with the theme and checks that the icon URL is
// new on the second load.
func openPage(t *testing.T, path, pageURL, theme, init string) (*chrome, string) {
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
	if init != "" {
		c.call(session, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": init})
	}
	load := func() string {
		loadPage(t, c, session, pageURL)
		var href string
		c.eval(session, iconHref, &href)
		return href
	}
	// The theme is chosen by the page script from localStorage, so it is
	// set on the origin first and the page loaded again.
	load()
	var ok bool
	c.eval(session, "(localStorage.setItem('theme', "+strconv.Quote(theme)+"), true)", &ok)
	first := load()
	// Safari keeps one icon per page URL and fetches only an icon URL it
	// has not cached, so the URL must be new on every load.
	if second := load(); second == first {
		t.Fatalf("the icon URL is the same on two loads: %s", first)
	}
	return c, session
}

// iconPixels reads the tab icon of the open page at the points.
func iconPixels(t *testing.T, c *chrome, session string, points ...[2]int) iconProbe {
	t.Helper()
	pts, err := json.Marshal(points)
	if err != nil {
		t.Fatal(err)
	}
	var p iconProbe
	c.eval(session, iconProbeJS+"("+string(pts)+")", &p)
	if p.Error != "" {
		t.Fatalf("%s (href %q)", p.Error, p.Href)
	}
	return p
}

// hexRGB parses a "#RRGGBB" color.
func hexRGB(t *testing.T, s string) [3]int {
	t.Helper()
	v, err := strconv.ParseUint(strings.TrimPrefix(s, "#"), 16, 32)
	if err != nil || len(s) != 7 {
		t.Fatalf("not a hex color: %q", s)
	}
	return [3]int{int(v >> 16), int(v >> 8 & 0xFF), int(v & 0xFF)}
}

// near reports whether a pixel is opaque and matches a color within a
// small tolerance.
func near(px [4]int, c [3]int) bool {
	for i := range c {
		if d := px[i] - c[i]; d < -3 || d > 3 {
			return false
		}
	}
	return px[3] == 255
}

// bluePNG is a square blue logo.
func bluePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 0x1E, 0x40, 0xAF, 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// dashboardServer is a running server with one monitor that is up and,
// when down is true, one more that is down. It returns the id of the
// monitor that is up.
func dashboardServer(t *testing.T, down bool) (*Server, *store.Store, int64) {
	t.Helper()
	seeds := []seed{{name: "Alpha"}}
	if down {
		seeds = append(seeds, seed{name: "Bravo", down: true})
	}
	s, st, ids := seedServer(t, seeds...)
	return s, st, ids[0]
}

// TestFaviconPixels renders the dashboard in headless Chrome and reads the
// pixels of the tab icon: for the built-in logo in both themes, for a PNG
// and an SVG logo, in both states and after a live change of state.
func TestFaviconPixels(t *testing.T) {
	path := requireChrome(t)
	up, down, blue := hexRGB(t, brand.UpColor), hexRGB(t, brand.DownColor), [3]int{0x1E, 0x40, 0xAF}
	// Points on the 64 pixel icon: the banner of the mark, the middle of a
	// logo and the red dot on an uploaded logo.
	banner, middle, dot := [2]int{32, 34}, [2]int{32, 32}, [2]int{51, 51}
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><rect width="8" height="8" fill="#1E40AF"/></svg>`)
	type pixel struct {
		at   [2]int
		want [3]int
	}
	tests := []struct {
		name   string
		logo   []byte // nil for the built-in logo
		down   bool
		theme  string
		pixels []pixel
	}{
		{"mark down light", nil, true, "light", []pixel{{banner, down}}},
		{"mark down dark", nil, true, "dark", []pixel{{banner, down}}},
		{"mark up light", nil, false, "light", []pixel{{banner, up}}},
		{"mark up dark", nil, false, "dark", []pixel{{banner, up}}},
		{"png logo down", bluePNG(t), true, "light", []pixel{{middle, blue}, {dot, down}}},
		{"png logo up", bluePNG(t), false, "light", []pixel{{middle, blue}, {dot, blue}}},
		{"svg logo down", svg, true, "light", []pixel{{middle, blue}, {dot, down}}},
		{"svg logo up", svg, false, "light", []pixel{{middle, blue}, {dot, blue}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, st, _ := dashboardServer(t, tc.down)
			ts, c := browserFixture(t, s, st)
			if tc.logo != nil {
				if res := postMultipart(t, c, ts.URL+"/settings", map[string]string{"name": "Acme", "accent": "#4F46E5"}, tc.logo); res.StatusCode != http.StatusOK {
					t.Fatalf("upload = %d", res.StatusCode)
				}
			}
			b := s.currentBrand()
			wantHref, wantTitle := b.FaviconUpURL, "Dashboard · "+b.Name
			if tc.down {
				wantHref, wantTitle = b.FaviconDownURL, "(1) "+wantTitle
			}
			page, session := openPage(t, path, ts.URL+"/", tc.theme, "")
			var points [][2]int
			for _, px := range tc.pixels {
				points = append(points, px.at)
			}
			p := iconPixels(t, page, session, points...)
			if p.Theme != tc.theme {
				t.Fatalf("page theme = %q, want %q", p.Theme, tc.theme)
			}
			if p.Title != wantTitle {
				t.Errorf("title = %q, want %q", p.Title, wantTitle)
			}
			if !strings.HasPrefix(p.Href, wantHref+"#") {
				t.Errorf("icon href = %q, want %q with a fragment", p.Href, wantHref)
			}
			for i, px := range tc.pixels {
				if !near(p.Pixels[i], px.want) {
					t.Errorf("pixel at %v = %v, want %v", px.at, p.Pixels[i], px.want)
				}
			}
		})
	}

	t.Run("live change", func(t *testing.T) {
		s, st, alpha := dashboardServer(t, false)
		ts, _ := browserFixture(t, s, st)
		page, session := openPage(t, path, ts.URL+"/", "light", liveOpen)
		// An event sent before the event stream is open is lost.
		waitLive(t, page, session)
		at := `document.querySelector('.mon-row[data-id="` + strconv.FormatInt(alpha, 10) + `"] .mon-checked').dataset.at`
		var before string
		page.eval(session, "String("+at+")", &before)
		// The first failed check proves that events reach the page. The
		// second one turns the monitor down.
		ctx := context.Background()
		if _, err := s.engine.CheckNow(ctx, alpha); err != nil {
			t.Fatal(err)
		}
		waitFor(t, page, session, "String("+at+") !== "+strconv.Quote(before), at, "the check event to change the checked time "+strconv.Quote(before))
		if _, err := s.engine.CheckNow(ctx, alpha); err != nil {
			t.Fatal(err)
		}
		b := s.currentBrand()
		waitForIcon(t, page, session, b.FaviconDownURL, "the down icon")
		p := iconPixels(t, page, session, banner)
		if !near(p.Pixels[0], down) {
			t.Errorf("banner pixel = %v, want %v", p.Pixels[0], down)
		}
		if want := "(1) Dashboard · " + b.Name; p.Title != want {
			t.Errorf("title = %q, want %q", p.Title, want)
		}
	})
}

// TestStatusIconPixels reads the tab icon of the public status page in
// headless Chrome as a visitor who is not logged in. A private monitor that
// is down must not turn it red, a public one must, and the minute refresh
// must swap it while the page is open.
func TestStatusIconPixels(t *testing.T) {
	path := requireChrome(t)
	banner := [2]int{32, 34}
	open := func(t *testing.T, privateDown, publicDown bool, init string) (*Server, int64, *chrome, string) {
		t.Helper()
		s, st, ids := seedServer(t, seed{name: "Private", down: privateDown}, seed{name: "Public", down: publicDown, public: true})
		setPassword(t, st)
		ts := httptest.NewServer(s)
		t.Cleanup(ts.Close)
		page, session := openPage(t, path, ts.URL+"/status", "light", init)
		return s, ids[1], page, session
	}
	check := func(t *testing.T, s *Server, page *chrome, session string, down bool) {
		t.Helper()
		b := s.currentBrand()
		href, color, title := b.FaviconUpURL, brand.UpColor, "Status · "+b.Name
		if down {
			href, color, title = b.FaviconDownURL, brand.DownColor, "(1) "+title
		}
		p := iconPixels(t, page, session, banner)
		if !strings.HasPrefix(p.Href, href+"#") {
			t.Errorf("icon href = %q, want %q with a fragment", p.Href, href)
		}
		if want := hexRGB(t, color); !near(p.Pixels[0], want) {
			t.Errorf("banner pixel = %v, want %v", p.Pixels[0], want)
		}
		if p.Title != title {
			t.Errorf("title = %q, want %q", p.Title, title)
		}
	}

	t.Run("private monitor down", func(t *testing.T) {
		s, _, page, session := open(t, true, false, "")
		check(t, s, page, session, false)
	})
	t.Run("public monitor down", func(t *testing.T) {
		s, _, page, session := open(t, false, true, "")
		check(t, s, page, session, true)
	})
	t.Run("refresh swaps the icon", func(t *testing.T) {
		s, public, page, session := open(t, true, false, fastRefresh)
		check(t, s, page, session, false)
		// Two failed checks turn the public monitor down.
		for i := 0; i < 2; i++ {
			if _, err := s.engine.CheckNow(context.Background(), public); err != nil {
				t.Fatal(err)
			}
		}
		waitForIcon(t, page, session, s.currentBrand().FaviconDownURL, "the refresh to swap the icon")
		check(t, s, page, session, true)
	})
}
