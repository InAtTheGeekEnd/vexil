package web

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

func TestMonitorDetailPage(t *testing.T) {
	s, st := newTestServer(t, Options{})
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
	// Give the restored engine state: the newest check is a success.
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
