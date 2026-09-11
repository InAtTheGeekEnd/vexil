package store

import (
	"context"
	"testing"
	"time"
)

func TestReorderMonitors(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	var ids []int64
	for _, n := range []string{"a", "b", "c"} {
		m := &Monitor{Name: n, Type: TypeTCP, Target: "x:1"}
		if err := s.CreateMonitor(ctx, m); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	if err := s.ReorderMonitors(ctx, []int64{ids[2], ids[0], ids[1]}); err != nil {
		t.Fatal(err)
	}
	all, err := s.Monitors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{all[0].Name, all[1].Name, all[2].Name}
	if got[0] != "c" || got[1] != "a" || got[2] != "b" {
		t.Fatalf("order = %v, want [c a b]", got)
	}
}

func TestDailyStatsAndLatencySeries(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	m := &Monitor{Name: "m", Type: TypeHTTP, Target: "https://x"}
	if err := s.CreateMonitor(ctx, m); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	checks := []Check{
		{At: now.Add(-30 * time.Minute), OK: true, LatencyMS: 100},
		{At: now.Add(-20 * time.Minute), OK: true, LatencyMS: 300},
		{At: now.Add(-10 * time.Minute), OK: false, Error: "HTTP 503"},
		{At: now.AddDate(0, 0, -1), OK: true, LatencyMS: 50},
		{At: now.AddDate(0, 0, -40), OK: false}, // outside the window
	}
	for _, c := range checks {
		c.MonitorID = m.ID
		if err := s.InsertCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO daily (monitor_id, day, total, ok) VALUES (?, ?, 10, 9)`,
		m.ID, now.AddDate(0, 0, -2).Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenIncident(ctx, m.ID, now.Add(-10*time.Minute), "HTTP 503"); err != nil {
		t.Fatal(err)
	}

	days, err := s.DailyStats(ctx, m.ID, now.AddDate(0, 0, -29), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 30 {
		t.Fatalf("got %d days, want 30", len(days))
	}
	tests := []struct {
		idx                  int
		total, ok, incidents int
	}{
		{29, 3, 2, 1},
		{28, 1, 1, 0},
		{27, 10, 9, 0},
		{0, 0, 0, 0},
	}
	for _, tt := range tests {
		d := days[tt.idx]
		if d.Total != tt.total || d.OK != tt.ok || d.Incidents != tt.incidents {
			t.Errorf("day %d (%s) = %+v, want total %d ok %d incidents %d", tt.idx, d.Day, d, tt.total, tt.ok, tt.incidents)
		}
	}
	if days[29].Day != "2026-09-11" || days[0].Day != "2026-08-13" {
		t.Errorf("day range = %s .. %s", days[0].Day, days[29].Day)
	}

	series, err := s.LatencySeries(ctx, m.ID, now.Add(-time.Hour), 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || series[0].LatencyMS != 200 {
		t.Fatalf("series = %+v, want one point of 200 ms", series)
	}
}

func TestSummary(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	m := &Monitor{Name: "m", Type: TypeHTTP, Target: "https://x"}
	if err := s.CreateMonitor(ctx, m); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	empty, err := s.Summary(ctx, m.ID, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := empty.Percent(); ok || empty.Total != 0 {
		t.Fatalf("empty summary = %+v", empty)
	}
	checks := []Check{
		{At: now.Add(-3 * time.Hour), OK: true, LatencyMS: 100}, // outside the window
		{At: now.Add(-30 * time.Minute), OK: true, LatencyMS: 100},
		{At: now.Add(-20 * time.Minute), OK: true, LatencyMS: 300},
		{At: now.Add(-10 * time.Minute), OK: false, Error: "HTTP 503"},
		{At: now.Add(-5 * time.Minute), OK: true, LatencyMS: 200},
	}
	for _, c := range checks {
		c.MonitorID = m.ID
		if err := s.InsertCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Summary(ctx, m.ID, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 4 || got.OK != 3 || got.AvgLatencyMS != 200 {
		t.Fatalf("summary = %+v, want total 4, ok 3, avg 200", got)
	}
	if pct, ok := got.Percent(); !ok || pct != 75 {
		t.Fatalf("percent = %v, %v", pct, ok)
	}
}
