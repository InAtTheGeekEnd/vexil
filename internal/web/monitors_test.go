package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/engine"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// loggedIn returns a running test server and a client with an admin session.
func loggedIn(t *testing.T, s *Server, st *store.Store) (*httptest.Server, *http.Client) {
	t.Helper()
	setPassword(t, st)
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	c := client(t)
	if res := postForm(t, c, ts.URL+"/login", url.Values{"password": {testPassword}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("login = %d", res.StatusCode)
	}
	return ts, c
}

// monitorID reads the id from a redirect to /monitors/{id}.
func monitorID(t *testing.T, res *http.Response) int64 {
	t.Helper()
	loc := res.Header.Get("Location")
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/monitors/") {
		t.Fatalf("got %d -> %q, want 303 -> /monitors/{id}", res.StatusCode, loc)
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(loc, "/monitors/"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMonitorLifecycle(t *testing.T) {
	s, st := newTestServer(t, Options{})
	ts, c := loggedIn(t, s, st)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello world")
	}))
	defer target.Close()
	events, stop := s.engine.Hub().Subscribe(64)
	defer stop()

	// Empty dashboard.
	if b := body(t, get(t, c, ts.URL+"/")); !strings.Contains(b, "No monitors yet") {
		t.Fatal("empty dashboard lacks the empty state")
	}

	// Create. The name is empty, so it comes from the target host.
	res := postForm(t, c, ts.URL+"/monitors/new", url.Values{
		"type": {"http"}, "url": {target.URL}, "keyword": {"hello"}, "interval": {"300"}, "public": {"1"},
	})
	id := monitorID(t, res)
	m, err := st.Monitor(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "127.0.0.1" || m.Keyword != "hello" || m.IntervalS != 300 || !m.Public {
		t.Fatalf("saved monitor = %+v", m)
	}

	// The first check ran at once.
	select {
	case ev := <-events:
		if ev.MonitorID != id || !ev.Result.OK || ev.State != engine.Up {
			t.Fatalf("first check event = %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no first check")
	}
	b := body(t, get(t, c, ts.URL+"/monitors/"+strconv.FormatInt(id, 10)))
	for _, want := range []string{"127.0.0.1", ">Up<", "HTTP(S)", "every 5 minutes", "ms ·", "Pause", "Delete", "/edit"} {
		if !strings.Contains(b, want) {
			t.Errorf("detail page lacks %q", want)
		}
	}

	// Dashboard shows the row.
	b = body(t, get(t, c, ts.URL+"/"))
	for _, want := range []string{"All systems operational", "1 monitor", "127.0.0.1", target.URL, "100<small>%", "Checked", `data-id="` + strconv.FormatInt(id, 10) + `"`} {
		if !strings.Contains(b, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}

	// Edit form is prefilled with the type fixed.
	b = body(t, get(t, c, ts.URL+"/monitors/"+strconv.FormatInt(id, 10)+"/edit"))
	if !strings.Contains(b, `value="`+target.URL+`"`) || !strings.Contains(b, `name="type" value="http"`) || strings.Contains(b, "type-cards") {
		t.Fatal("edit form is not prefilled or shows the type cards")
	}
	res = postForm(t, c, ts.URL+"/monitors/"+strconv.FormatInt(id, 10)+"/edit", url.Values{
		"type": {"tcp"}, "name": {"Renamed"}, "url": {target.URL}, "interval": {"60"},
	})
	if got := monitorID(t, res); got != id {
		t.Fatalf("edit redirected to %d", got)
	}
	m, _ = st.Monitor(context.Background(), id)
	if m.Name != "Renamed" || m.Type != store.TypeHTTP || m.IntervalS != 60 || m.Public {
		t.Fatalf("edited monitor = %+v", m)
	}

	// Pause and resume.
	res = postForm(t, c, ts.URL+"/monitors/"+strconv.FormatInt(id, 10)+"/pause", nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("pause = %d", res.StatusCode)
	}
	b = body(t, get(t, c, ts.URL+"/monitors/"+strconv.FormatInt(id, 10)))
	if !strings.Contains(b, ">Paused<") || !strings.Contains(b, "Resume") {
		t.Fatal("detail page does not show the paused state")
	}
	if st, _ := s.engine.Status(id); st.State != engine.Paused {
		t.Fatalf("engine state after pause = %s", st.State)
	}
	postForm(t, c, ts.URL+"/monitors/"+strconv.FormatInt(id, 10)+"/resume", nil)
	if m, _ = st.Monitor(context.Background(), id); m.Paused {
		t.Fatal("monitor still paused after resume")
	}

	// Delete.
	res = postForm(t, c, ts.URL+"/monitors/"+strconv.FormatInt(id, 10)+"/delete", nil)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/" {
		t.Fatalf("delete = %d -> %q", res.StatusCode, res.Header.Get("Location"))
	}
	if res := get(t, c, ts.URL+"/monitors/"+strconv.FormatInt(id, 10)); res.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted monitor = %d, want 404", res.StatusCode)
	}
	if _, ok := s.engine.Status(id); ok {
		t.Fatal("engine still tracks the deleted monitor")
	}
}

func TestMonitorFormValidation(t *testing.T) {
	s, st := newTestServer(t, Options{})
	ts, c := loggedIn(t, s, st)

	tests := []struct {
		name string
		form url.Values
		want string
	}{
		{"bad type", url.Values{"type": {"ftp"}, "interval": {"60"}}, "Choose a type"},
		{"missing url", url.Values{"type": {"http"}, "interval": {"60"}}, "starts with http://"},
		{"url without scheme", url.Values{"type": {"http"}, "url": {"example.com"}, "interval": {"60"}}, "starts with http://"},
		{"tcp without host", url.Values{"type": {"tcp"}, "port": {"80"}, "interval": {"60"}}, "Enter a host name"},
		{"tcp bad port", url.Values{"type": {"tcp"}, "host": {"db"}, "port": {"70000"}, "interval": {"60"}}, "Enter a number between 1 and 65535."},
		{"ping without host", url.Values{"type": {"ping"}, "interval": {"60"}}, "Enter a host name"},
		{"dns bad ip", url.Values{"type": {"dns"}, "hostname": {"example.com"}, "expected_ip": {"nope"}, "interval": {"60"}}, "valid IP address"},
		{"tcp host with a port", url.Values{"type": {"tcp"}, "host": {"db.example.com:5432"}, "port": {"5432"}, "interval": {"60"}}, "without http:// or a port"},
		{"tcp host with a scheme", url.Values{"type": {"tcp"}, "host": {"https://db.example.com"}, "port": {"443"}, "interval": {"60"}}, "without http:// or a port"},
		{"ping host with a scheme", url.Values{"type": {"ping"}, "host": {"https://example.com"}, "interval": {"60"}}, "without http:// or a port"},
		{"ping host with a port", url.Values{"type": {"ping"}, "host": {"example.com:80"}, "interval": {"60"}}, "without http:// or a port"},
		{"ping bracketed ipv6", url.Values{"type": {"ping"}, "host": {"[::1]"}, "interval": {"60"}}, "without http:// or a port"},
		{"dns hostname with a path", url.Values{"type": {"dns"}, "hostname": {"https://example.com/x"}, "interval": {"60"}}, "without http:// or a port"},
		{"dns hostname with a port", url.Values{"type": {"dns"}, "hostname": {"example.com:53"}, "interval": {"60"}}, "without http:// or a port"},
		{"dns bare ipv4 address", url.Values{"type": {"dns"}, "hostname": {"93.184.215.14"}, "interval": {"60"}}, "not an IP address"},
		{"dns bare ipv6 address", url.Values{"type": {"dns"}, "hostname": {"2001:db8::1"}, "interval": {"60"}}, "not an IP address"},
		{"bad interval", url.Values{"type": {"push"}, "name": {"Job"}, "interval": {"45"}}, "Choose an interval"},
		{"push without name", url.Values{"type": {"push"}, "interval": {"60"}}, "Enter a name"},
		{"name too long", url.Values{"type": {"push"}, "name": {strings.Repeat("a", 61)}, "interval": {"60"}}, "60 characters or fewer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := postForm(t, c, ts.URL+"/monitors/new", tt.form)
			b := body(t, res)
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", res.StatusCode)
			}
			if !strings.Contains(b, tt.want) {
				t.Fatalf("page lacks %q", tt.want)
			}
		})
	}
	if all, _ := st.Monitors(context.Background()); len(all) != 0 {
		t.Fatalf("invalid forms created %d monitors", len(all))
	}

	// Valid TCP and DNS forms build the right target.
	res := postForm(t, c, ts.URL+"/monitors/new", url.Values{"type": {"tcp"}, "host": {"127.0.0.1"}, "port": {"1"}, "interval": {"30"}})
	m, _ := st.Monitor(context.Background(), monitorID(t, res))
	if m.Target != "127.0.0.1:1" || m.Name != "127.0.0.1" || m.IntervalS != 30 {
		t.Fatalf("tcp monitor = %+v", m)
	}
	res = postForm(t, c, ts.URL+"/monitors/new", url.Values{"type": {"dns"}, "hostname": {"localhost"}, "expected_ip": {"127.0.0.1"}, "interval": {"86400"}})
	m, _ = st.Monitor(context.Background(), monitorID(t, res))
	if m.Target != "localhost" || m.ExpectedIP != "127.0.0.1" || m.IntervalS != 86400 {
		t.Fatalf("dns monitor = %+v", m)
	}
	// A bare IPv6 address is a valid ping target.
	res = postForm(t, c, ts.URL+"/monitors/new", url.Values{"type": {"ping"}, "host": {"::1"}, "interval": {"60"}})
	m, _ = st.Monitor(context.Background(), monitorID(t, res))
	if m.Target != "::1" {
		t.Fatalf("ping monitor = %+v", m)
	}
}

func TestPushMonitorDetail(t *testing.T) {
	s, st := newTestServer(t, Options{BaseURL: "https://status.example.com/"})
	ts, c := loggedIn(t, s, st)

	res := postForm(t, c, ts.URL+"/monitors/new", url.Values{"type": {"push"}, "name": {"Nightly backup"}, "interval": {"900"}})
	id := monitorID(t, res)
	m, _ := st.Monitor(context.Background(), id)
	b := body(t, get(t, c, ts.URL+"/monitors/"+strconv.FormatInt(id, 10)))
	want := "https://status.example.com/push/" + m.PushToken
	if !strings.Contains(b, want) || !strings.Contains(b, "curl -fsS "+want) {
		t.Fatalf("detail page lacks the push URL %q", want)
	}
	if !strings.Contains(b, "Waiting for the first push") {
		t.Fatal("detail page lacks the pending push line")
	}
	b = body(t, get(t, c, ts.URL+"/"))
	if !strings.Contains(b, "Heartbeat every 15 minutes") || !strings.Contains(b, "Waiting for the first checks") {
		t.Fatal("dashboard lacks the push row or the pending headline")
	}
}

func TestDashboardOrderAndReorder(t *testing.T) {
	// Build the store first so the engine restores DOWN from the database.
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	now := time.Now()
	var ids []int64
	for _, name := range []string{"Alpha", "Bravo", "Charlie"} {
		m := &store.Monitor{Name: name, Type: store.TypeTCP, Target: "127.0.0.1:1", IntervalS: 900}
		if err := st.CreateMonitor(ctx, m); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	// Alpha is up, Bravo is down (two failures), Charlie is paused.
	for _, c := range []store.Check{
		{MonitorID: ids[0], At: now.Add(-time.Minute), OK: true, LatencyMS: 12},
		{MonitorID: ids[0], At: now.Add(-2 * time.Minute), OK: true, LatencyMS: 20},
		{MonitorID: ids[1], At: now.Add(-time.Minute), Error: "connection refused"},
		{MonitorID: ids[1], At: now.Add(-2 * time.Minute), Error: "connection refused"},
	} {
		if err := st.InsertCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetPaused(ctx, ids[2], true); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := engine.New(st, engine.Options{Log: log})
	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Stop)
	s, err := New(st, Options{Log: log, Engine: eng})
	if err != nil {
		t.Fatal(err)
	}
	ts, c := loggedIn(t, s, st)

	b := body(t, get(t, c, ts.URL+"/"))
	if !strings.Contains(b, "1 monitor is down") {
		t.Fatal("headline does not report the down monitor")
	}
	bravo, alpha, charlie := strings.Index(b, "Bravo"), strings.Index(b, "Alpha"), strings.Index(b, "Charlie")
	if bravo < 0 || alpha < 0 || charlie < 0 || !(bravo < alpha && alpha < charlie) {
		t.Fatalf("row order: Bravo %d Alpha %d Charlie %d, want Bravo first", bravo, alpha, charlie)
	}
	for _, want := range []string{"mon-down", "0<small>%", "100<small>%", ">Paused<", "seg seg-up", "seg seg-down"} {
		if !strings.Contains(b, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}

	// Reorder: Charlie, Alpha, Bravo, all in no group. Bravo is down, so the
	// save skips it: it keeps position 2, and the others skip that position.
	// Down still sorts first on the page.
	res := postForm(t, c, ts.URL+"/monitors/reorder", url.Values{
		"id": {strconv.FormatInt(ids[2], 10), strconv.FormatInt(ids[0], 10), strconv.FormatInt(ids[1], 10)},
		"in": {"0", "0", "0"},
	})
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("reorder = %d", res.StatusCode)
	}
	all, _ := st.Monitors(ctx)
	if all[0].Name != "Charlie" || all[1].Name != "Bravo" || all[2].Name != "Alpha" || all[1].Position != 2 {
		t.Fatalf("stored order = %s %s %s", all[0].Name, all[1].Name, all[2].Name)
	}
	b = body(t, get(t, c, ts.URL+"/"))
	bravo, alpha, charlie = strings.Index(b, "Bravo"), strings.Index(b, "Alpha"), strings.Index(b, "Charlie")
	if !(bravo < charlie && charlie < alpha) {
		t.Fatalf("row order after reorder: Bravo %d Charlie %d Alpha %d", bravo, charlie, alpha)
	}
	if res := postForm(t, c, ts.URL+"/monitors/reorder", url.Values{"id": {"x"}}); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad reorder id = %d, want 400", res.StatusCode)
	}

	// Every monitor route needs a login.
	anon := client(t)
	for _, p := range []string{"/monitors/new", "/monitors/" + strconv.FormatInt(ids[0], 10), "/monitors/" + strconv.FormatInt(ids[0], 10) + "/edit"} {
		if res := get(t, anon, ts.URL+p); res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/login" {
			t.Errorf("anonymous GET %s = %d -> %q", p, res.StatusCode, res.Header.Get("Location"))
		}
	}
	if res := postForm(t, anon, ts.URL+"/monitors/reorder", url.Values{"id": {"1"}}); res.StatusCode != http.StatusFound {
		t.Errorf("anonymous reorder = %d, want 302", res.StatusCode)
	}
	if res := get(t, c, ts.URL+"/monitors/999"); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown monitor = %d, want 404", res.StatusCode)
	}
}

func TestAgoAndHeadline(t *testing.T) {
	agoTests := []struct {
		d    time.Duration
		want string
	}{
		{2 * time.Second, "just now"},
		{12 * time.Second, "12 s ago"},
		{3 * time.Minute, "3 m ago"},
		{2 * time.Hour, "2 h ago"},
		{72 * time.Hour, "3 d ago"},
	}
	for _, tt := range agoTests {
		if got := ago(tt.d); got != tt.want {
			t.Errorf("ago(%s) = %q, want %q", tt.d, got, tt.want)
		}
	}
	headTests := []struct {
		down, pending, active int
		want, state           string
	}{
		{0, 0, 3, "All systems operational", "up"},
		{1, 0, 3, "1 monitor is down", "down"},
		{2, 1, 3, "2 monitors are down", "down"},
		{0, 2, 2, "Waiting for the first checks", "pending"},
		{0, 0, 0, "All monitors are paused", "paused"},
	}
	for _, tt := range headTests {
		got, state := headline(tt.down, tt.pending, tt.active)
		if got != tt.want || state != tt.state {
			t.Errorf("headline(%d,%d,%d) = %q %q, want %q %q", tt.down, tt.pending, tt.active, got, state, tt.want, tt.state)
		}
	}
}
