package store

import (
	"context"
	"testing"
	"time"
)

func TestLatencySeries(t *testing.T) {
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
		{At: now.AddDate(0, 0, -1), OK: true, LatencyMS: 50}, // outside the window
	}
	for _, c := range checks {
		c.MonitorID = m.ID
		if err := s.InsertCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	series, err := s.LatencySeries(ctx, m.ID, now.Add(-time.Hour), 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || series[0].LatencyMS != 200 {
		t.Fatalf("series = %+v, want one point of 200 ms", series)
	}
}

// TestRollupAndRetainRows runs the job as it ran yesterday and then Retain,
// with checks on an expired day and on the cutoff day. The expired day
// keeps only its daily row. The cutoff day keeps its checks, its hourly row
// and its daily row. Both daily rows average the successful checks.
func TestRollupAndRetainRows(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	m := &Monitor{Name: "m", Type: TypeHTTP, Target: "https://x", IntervalS: 60}
	if err := s.CreateMonitor(ctx, m); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	today := now.Truncate(24 * time.Hour)
	day := func(d int) time.Time { return today.AddDate(0, 0, d).Add(time.Hour) }

	seedChecks(t, s, m.ID, day(-45), 10, true)
	seedChecks(t, s, m.ID, day(-30), 10, true)
	seedChecks(t, s, m.ID, day(-30).Add(10*time.Minute), 10, false)
	if err := s.Rollup(ctx, now.AddDate(0, 0, -1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retain(ctx, now, retentionBatch); err != nil {
		t.Fatal(err)
	}
	if n := countChecks(t, s, m.ID); n != 20 {
		t.Fatalf("checks after Retain = %d, want the 20 of the cutoff day", n)
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := s.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	tests := []struct {
		name  string
		query string
		args  []any
		want  int
	}{
		{"daily rows", `SELECT COUNT(*) FROM daily WHERE monitor_id = ?`, []any{m.ID}, 2},
		{"daily rows at 100 ms", `SELECT COUNT(*) FROM daily WHERE monitor_id = ? AND avg_latency = 100`, []any{m.ID}, 2},
		{"hourly rows of the expired day", `SELECT COUNT(*) FROM hourly WHERE monitor_id = ? AND hour < ?`, []any{m.ID, day(-44).Unix()}, 0},
		{"hourly rows of the cutoff day", `SELECT COUNT(*) FROM hourly WHERE monitor_id = ? AND hour >= ?`, []any{m.ID, day(-30).Unix()}, 1},
		{"hourly rows at 100 ms", `SELECT COUNT(*) FROM hourly WHERE monitor_id = ? AND avg_latency = 100`, []any{m.ID}, 1},
	}
	for _, tc := range tests {
		if got := count(tc.query, tc.args...); got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, got, tc.want)
		}
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
	if empty.OK != 0 || empty.AvgLatencyMS != 0 {
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
	if got.OK != 3 || got.AvgLatencyMS != 200 {
		t.Fatalf("summary = %+v, want ok 3, avg 200", got)
	}
}
