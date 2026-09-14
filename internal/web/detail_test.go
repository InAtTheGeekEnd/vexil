package web

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// TestDetailChartsReadHourly runs the job as it ran two hours ago, so the
// hourly rows end three hours before the current hour. Then checks with
// other latencies go in behind two of the rows, and one check goes in an
// hour after the newest row. The 24-hour chart must show the checks. The
// 7-day chart must show the row and not the checks behind it, and the hour
// after the newest row from its check. The 30-day chart must weight each
// hour of a 6-hour bucket by its successful checks: 57 at 100 ms and 2 at
// 200 ms give 103 ms, not the 150 ms of a plain average.
func TestDetailChartsReadHourly(t *testing.T) {
	s, st := newIdleServer(t, Options{})
	ctx := context.Background()
	m := &store.Monitor{Name: "Shop", Type: store.TypeHTTP, Target: "https://shop.example.com", IntervalS: 60}
	if err := st.CreateMonitor(ctx, m); err != nil {
		t.Fatal(err)
	}
	add := func(start time.Time, n, ok int, latency int64) {
		t.Helper()
		for i := 0; i < n; i++ {
			c := store.Check{MonitorID: m.ID, At: start.Add(time.Duration(i) * time.Second), OK: i < ok, LatencyMS: latency}
			if i >= ok {
				c.Error = "HTTP 503"
			}
			if err := st.InsertCheck(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
	}
	now := time.Now()
	current := now.UTC().Truncate(time.Hour)
	old := current.Truncate(24*time.Hour).AddDate(0, 0, -10)
	add(old, 60, 57, 100)
	add(old.Add(time.Hour), 2, 2, 200)
	add(current.Add(-3*time.Hour), 2, 2, 120)
	if err := st.Rollup(ctx, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	add(old.Add(30*time.Minute), 57, 57, 999)
	add(current.Add(-3*time.Hour+20*time.Minute), 2, 2, 999)
	add(current.Add(-time.Hour+time.Minute), 1, 1, 300)

	ts, c := loggedIn(t, s, st)
	b := body(t, get(t, c, ts.URL+"/monitors/"+strconv.FormatInt(m.ID, 10)))
	charts := regexp.MustCompile(`data-points="([^"]*)"`).FindAllStringSubmatch(b, -1)
	if len(charts) != len(chartRanges) {
		t.Fatalf("detail page has %d charts, want %d", len(charts), len(chartRanges))
	}
	tests := []struct {
		name  string
		chart int // index into chartRanges: 24h, 7d, 30d
		label string
		want  bool
	}{
		{"24-hour chart reads the checks", 0, "· 999 ms", true},
		{"7-day chart reads the hourly row", 1, "· 120 ms", true},
		{"7-day chart skips the checks behind a row", 1, "· 999 ms", false},
		{"7-day chart reads the hour after the newest row from checks", 1, "· 300 ms", true},
		{"30-day chart weights the hours by successful checks", 2, "· 103 ms", true},
	}
	for _, tc := range tests {
		if got := strings.Contains(charts[tc.chart][1], tc.label); got != tc.want {
			t.Errorf("%s: %s chart has %q = %v, want %v", tc.name, chartRanges[tc.chart].Key, tc.label, got, tc.want)
		}
	}
}

func TestMonitorDetailPage(t *testing.T) {
	// The engine never starts, so no check of its own can change the page
	// before the test reads it.
	s, st := newIdleServer(t, Options{})
	ctx := context.Background()
	m := &store.Monitor{Name: "Shop", Type: store.TypeHTTP, Target: "https://shop.example.com", IntervalS: 60}
	if err := st.CreateMonitor(ctx, m); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// Two days of checks: all fine, then an incident of ten minutes, then fine.
	for i := 48 * 60; i > 0; i -= 5 {
		at := now.Add(-time.Duration(i) * time.Minute)
		c := store.Check{MonitorID: m.ID, At: at, OK: true, LatencyMS: 100 + int64(i%7)*10}
		if i <= 130 && i > 120 {
			c = store.Check{MonitorID: m.ID, At: at, OK: false, Error: "HTTP 503"}
		}
		if err := st.InsertCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.OpenIncident(ctx, m.ID, now.Add(-130*time.Minute), "HTTP 503"); err != nil {
		t.Fatal(err)
	}
	if err := st.CloseIncident(ctx, m.ID, now.Add(-120*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.engine.Reload(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	// Reload registers the monitor as pending, as the create form does.
	ts, c := loggedIn(t, s, st)
	b := body(t, get(t, c, ts.URL+"/monitors/"+strconv.FormatInt(m.ID, 10)))
	for _, want := range []string{
		">Shop<", "HTTP(S)", "shop.example.com",
		"Uptime · 24 h", "Uptime · 7 d", "Uptime · 30 d", "Uptime · 90 d", "Avg response", "Certificate",
		`data-range="24h"`, `data-range="7d"`, `data-range="30d"`, `<svg class="chart"`,
		"Uptime · 90 days", "over 90 days",
		"Incidents", "HTTP 503", "10 m",
		"Pause", "Delete", "/edit",
		"Waiting for the first check", // the engine has not run a check of its own yet
	} {
		if !strings.Contains(b, want) {
			t.Errorf("detail page lacks %q", want)
		}
	}
	if got := strings.Count(b, `<span class="seg`); got != 90 {
		t.Errorf("uptime bar has %d segments, want 90", got)
	}
	if !strings.Contains(b, `<body class="" data-live data-down="0">`) {
		t.Error("detail page is not live")
	}
	// The 24 h uptime tile is below 100 and the avg response tile has a value.
	if strings.Contains(b, `<span class="label">Uptime · 24 h</span><span class="value">100<small>`) {
		t.Error("24 h uptime shows 100% despite the incident")
	}
	if !strings.Contains(b, `<span class="label">Uptime · 90 d</span><span class="value">`) {
		t.Error("90 d uptime tile has no value")
	}
	if strings.Contains(b, `<span class="label">Avg response</span><span class="value value-empty">`) {
		t.Error("avg response tile is empty")
	}
}

func TestMonitorDetailEmpty(t *testing.T) {
	s, st := newTestServer(t, Options{})
	m := &store.Monitor{Name: "Plain", Type: store.TypeTCP, Target: "db.example.com:5432", IntervalS: 60}
	if err := st.CreateMonitor(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	ts, c := loggedIn(t, s, st)
	b := body(t, get(t, c, ts.URL+"/monitors/"+strconv.FormatInt(m.ID, 10)))
	for _, want := range []string{"No data", "Not enough data yet", "No incidents yet", "No data yet"} {
		if !strings.Contains(b, want) {
			t.Errorf("empty detail page lacks %q", want)
		}
	}
	if strings.Contains(b, "Certificate") {
		t.Error("TCP monitor shows a certificate tile")
	}
}

func TestDurations(t *testing.T) {
	long := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "less than a minute"},
		{time.Minute, "1 minute"},
		{6 * time.Minute, "6 minutes"},
		{3 * time.Hour, "3 hours"},
		{14 * 24 * time.Hour, "14 days"},
	}
	for _, tc := range long {
		if got := longDuration(tc.d); got != tc.want {
			t.Errorf("longDuration(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
	short := []struct {
		d    time.Duration
		want string
	}{
		{42 * time.Second, "42 s"},
		{6 * time.Minute, "6 m"},
		{2*time.Hour + 10*time.Minute, "2 h 10 m"},
		{2 * time.Hour, "2 h"},
		{3*24*time.Hour + 4*time.Hour, "3 d 4 h"},
		{3 * 24 * time.Hour, "3 d"},
	}
	for _, tc := range short {
		if got := shortDuration(tc.d); got != tc.want {
			t.Errorf("shortDuration(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
