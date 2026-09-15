package store

import (
	"context"
	"testing"
	"time"
)

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

// TestDailyStatsSources reads 46 days after the job ran yesterday and
// Retain ran today. A day comes from its daily row: the expired day that
// Retain rolled up, and the cutoff day, whose checks Retain keeps. A day of
// the last 30 days without a daily row, and today, come from the checks
// through the hours. An older day without a daily row reads no checks: in
// use, Retain rolls it up before its checks can be read.
func TestDailyStatsSources(t *testing.T) {
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
	seedChecks(t, s, m.ID, day(-40), 10, true)
	seedChecks(t, s, m.ID, day(-29), 10, true)
	seedChecks(t, s, m.ID, now.Add(-2*time.Hour), 6, true)

	stats, err := s.DailyStats(ctx, m.ID, today.AddDate(0, 0, -45), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 46 {
		t.Fatalf("got %d days, want 46", len(stats))
	}
	tests := []struct {
		name      string
		day       int // days before today
		total, ok int
	}{
		{"expired day from the daily row of Retain", -45, 10, 10},
		{"older day without a daily row reads no checks", -40, 0, 0},
		{"cutoff day from its daily row", -30, 20, 10},
		{"day of the last 30 days without a daily row", -29, 10, 10},
		{"today", 0, 6, 6},
	}
	for _, tc := range tests {
		d := stats[45+tc.day]
		if d.Total != tc.total || d.OK != tc.ok {
			t.Errorf("%s: %s = %d checks, %d ok; want %d, %d", tc.name, d.Day, d.Total, d.OK, tc.total, tc.ok)
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
	if empty.Total != 0 || empty.AvgLatencyMS != 0 {
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
}
