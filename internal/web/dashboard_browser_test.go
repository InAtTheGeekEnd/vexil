//go:build chrome

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// TestStripOrderInChrome takes two monitors down live, the one lower on the
// page first. The newer outage must go to the top of the strip, and a reload
// must show the same order as the live page.
func TestStripOrderInChrome(t *testing.T) {
	path := requireChrome(t)
	s, st, ids := seedServer(t, seed{name: "Alpha"}, seed{name: "Bravo"})
	ts, _ := browserFixture(t, s, st)
	page, session := openPage(t, path, ts.URL+"/", "light", liveOpen)
	ctx := context.Background()
	const strip = "Array.from(document.querySelectorAll('.mon-strip .mon-row')).map(function (r) { return r.dataset.id; }).join(',')"
	alpha, bravo := strconv.FormatInt(ids[0], 10), strconv.FormatInt(ids[1], 10)

	// goDown fails two checks of a monitor and waits for the strip. The
	// first failure also proves that events reach the page.
	goDown := func(id, want string) {
		t.Helper()
		n, _ := strconv.ParseInt(id, 10, 64)
		// An event sent before the event stream is open is lost.
		waitLive(t, page, session)
		at := `document.querySelector('.mon-row[data-id="` + id + `"] .mon-checked').dataset.at`
		var before string
		page.eval(session, "String("+at+")", &before)
		if _, err := s.engine.CheckNow(ctx, n); err != nil {
			t.Fatal(err)
		}
		waitFor(t, page, session, "String("+at+") !== "+strconv.Quote(before), at, "the check event to change the checked time "+strconv.Quote(before))
		if _, err := s.engine.CheckNow(ctx, n); err != nil {
			t.Fatal(err)
		}
		waitForValue(t, page, session, strip, want, "the strip")
	}
	goDown(bravo, bravo)
	// Outage starts are stored to the second, so the clock must pass the
	// second in which the Bravo outage started.
	poll(t, "a new second after the start of the Bravo outage", func() (bool, string) {
		inc, err := st.CurrentIncident(ctx, ids[1])
		if err != nil {
			return false, "no open outage: " + err.Error()
		}
		now := time.Now()
		return now.Unix() > inc.StartedAt.Unix(), "start " + inc.StartedAt.Format(time.TimeOnly) + ", clock " + now.Format("15:04:05.000")
	})
	goDown(alpha, alpha+","+bravo)

	var live, reloaded string
	page.eval(session, strip, &live)
	loadPage(t, page, session, ts.URL+"/")
	page.eval(session, strip, &reloaded)
	if want := alpha + "," + bravo; live != want || reloaded != want {
		t.Fatalf("strip live = %s, after a reload = %s, want %s for both", live, reloaded, want)
	}
}

// TestStaleWhenSessionEndsInChrome ends the session while the dashboard is
// open and drops the event stream. The stream then gets 401, so the page
// must gray its dots, stop the pulse and say that its data is old. A new
// login lets the stream open again, and the stale state must go.
func TestStaleWhenSessionEndsInChrome(t *testing.T) {
	path := requireChrome(t)
	s, st, _ := seedServer(t, seed{name: "Alpha"})
	setPassword(t, st)

	// The wrapper adds the current session to each request, can end every
	// open stream as a lost connection would, and counts refused streams.
	var mu sync.Mutex
	var cookies []*http.Cookie
	drop := make(chan struct{})
	var refused atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		for _, ck := range cookies {
			r.AddCookie(ck)
		}
		stop := drop
		mu.Unlock()
		if r.URL.Path != "/events" {
			s.ServeHTTP(w, r)
			return
		}
		rctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		go func() {
			select {
			case <-stop:
				cancel()
			case <-rctx.Done():
			}
		}()
		cw := &codeWriter{ResponseWriter: w}
		s.ServeHTTP(cw, r.WithContext(rctx))
		if cw.code == http.StatusUnauthorized {
			refused.Add(1)
		}
	}))
	t.Cleanup(ts.Close)
	login := func() {
		t.Helper()
		c := client(t)
		if res := postForm(t, c, ts.URL+"/login", url.Values{"password": {testPassword}}); res.StatusCode != http.StatusSeeOther {
			t.Fatalf("login = %d", res.StatusCode)
		}
		u, _ := url.Parse(ts.URL)
		mu.Lock()
		cookies = c.Jar.Cookies(u)
		mu.Unlock()
	}
	login()

	page, session := openPage(t, path, ts.URL+"/", "light", liveOpen)
	waitLive(t, page, session)
	const stale = "document.body.classList.contains('stale') && !!document.querySelector('.stale-line')"
	var fresh bool
	if page.eval(session, "!("+stale+")", &fresh); !fresh {
		t.Fatal("the page is stale while its stream is open")
	}

	setPassword(t, st) // a password change ends every session
	mu.Lock()
	close(drop)
	drop = make(chan struct{})
	mu.Unlock()
	poll(t, "a refused stream after the session ended", func() (bool, string) {
		n := refused.Load()
		return n > 0, strconv.Itoa(int(n)) + " refused"
	})

	dot := "document.querySelector('.mon-row .dot')"
	waitFor(t, page, session, stale, "document.body.className", "the stale state")
	checks := map[string]string{
		// --paused in the light theme is #9CA3AF.
		"getComputedStyle(" + dot + ").backgroundColor":                       "rgb(156, 163, 175)",
		"getComputedStyle(" + dot + ", '::before').animationName":             "none",
		"getComputedStyle(document.querySelector('h1 .dot')).backgroundColor": "rgb(156, 163, 175)",
		"document.querySelector('.stale-line a').getAttribute('href')":        "/login",
		"document.querySelector('.stale-line').textContent":                   "The connection is lost. The data on this page is old.Log in again",
	}
	for expr, want := range checks {
		var got string
		if page.eval(session, "String("+expr+")", &got); got != want {
			t.Errorf("%s = %q, want %q", expr, got, want)
		}
	}

	// A new session lets the retry open the stream, which clears the stale
	// state. The retry waits up to 20 seconds.
	var ok bool
	page.eval(session, "(window.probeLive = false, true)", &ok)
	login()
	waitFor(t, page, session, "window.probeLive === true && !("+stale+")", "document.body.className", "the stream to open again and the stale state to go")
}

// codeWriter records the status code of a response.
type codeWriter struct {
	http.ResponseWriter
	code int
}

func (w *codeWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *codeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// TestGroupDropTargetInChrome drags a group with the mouse and drops it over
// the rows of another group, not over its heading. The group must land
// there, and the order must be saved.
func TestGroupDropTargetInChrome(t *testing.T) {
	path := requireChrome(t)
	s, st, ids := seedServer(t, seed{name: "Site"}, seed{name: "Inbox"}, seed{name: "Relay"}, seed{name: "Backup"})
	ctx := context.Background()
	groups := map[string]int64{}
	for _, name := range []string{"Web", "Mail", "Spare"} {
		g := &store.Group{Name: name}
		if err := st.CreateGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
		groups[name] = g.ID
	}
	if err := st.SaveLayout(ctx, nil, []store.Placement{
		{ID: ids[0], GroupID: groups["Web"]},
		{ID: ids[1], GroupID: groups["Mail"]},
		{ID: ids[2], GroupID: groups["Mail"]},
		{ID: ids[3], GroupID: groups["Spare"]},
	}); err != nil {
		t.Fatal(err)
	}
	ts, _ := browserFixture(t, s, st)
	page, session := openPage(t, path, ts.URL+"/", "light", "")
	// A tall viewport keeps every group on the screen for the mouse.
	page.call(session, "Emulation.setDeviceMetricsOverride", map[string]any{"width": 1280, "height": 1400, "deviceScaleFactor": 1, "mobile": false})

	const order = "Array.from(document.querySelectorAll('.dash-group h2')).map(function (h) { return h.textContent; }).join(',')"
	group := func(name string) string {
		return `.dash-group[data-group="` + strconv.FormatInt(groups[name], 10) + `"]`
	}
	middle := func(selector string) (x, y float64) {
		var p struct{ X, Y float64 }
		page.eval(session, "(function () { var r = document.querySelector("+strconv.Quote(selector)+").getBoundingClientRect(); return {X: r.left + r.width / 2, Y: r.top + r.height / 2}; })()", &p)
		return p.X, p.Y
	}
	mouse := func(kind string, x, y float64, buttons int) {
		page.call(session, "Input.dispatchMouseEvent", map[string]any{"type": kind, "x": x, "y": y, "button": "left", "buttons": buttons, "clickCount": 1})
	}
	// saved is the group order in the store.
	saved := func() string {
		gs, err := st.Groups(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, g := range gs {
			names = append(names, g.Name)
		}
		return strings.Join(names, ",")
	}

	for _, tt := range []struct {
		name, drag, over, want string
	}{
		{"up over the rows of the group above", "Spare", group("Mail") + " .mon-row:last-child", "Web,Spare,Mail"},
		{"down over the rows of the group below", "Web", group("Mail") + " .mon-row:first-child", "Spare,Mail,Web"},
	} {
		// Press the grip, move a few pixels toward the target to set the
		// direction, then jump to the middle of the row and drop there.
		x, y := middle(group(tt.drag) + " .group-handle")
		_, to := middle(tt.over)
		step := 6.0
		if to < y {
			step = -6
		}
		mouse("mousePressed", x, y, 1)
		mouse("mouseMoved", x, y+step, 1)
		mouse("mouseMoved", x, to, 1)
		mouse("mouseReleased", x, to, 0)
		waitForValue(t, page, session, order, tt.want, tt.name+": the order")
		poll(t, tt.name+": the saved order "+strconv.Quote(tt.want), func() (bool, string) {
			got := saved()
			return got == tt.want, got
		})
		loadPage(t, page, session, ts.URL+"/")
		waitForValue(t, page, session, order, tt.want, tt.name+": the order after a reload")
	}
}
