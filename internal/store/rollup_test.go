package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// TestRollupHours writes checks in the hour before now and in the current
// hour, for a monitor with latencies and one with failures only. The job
// must write the finished hour, with the average latency of the successful
// checks as an integer, and no row for the current hour. After a late check
// it must rewrite the finished hour. The job runs 30 seconds after every
// full UTC hour.
func TestRollupHours(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	var ids []int64
	for _, name := range []string{"A", "B"} {
		m := &Monitor{Name: name, Type: TypeHTTP, Target: "https://example.com", IntervalS: 60}
		if err := s.CreateMonitor(ctx, m); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	now := time.Date(2026, 9, 14, 12, 20, 0, 0, time.UTC)
	finished, current := now.Truncate(time.Hour).Add(-time.Hour), now.Truncate(time.Hour)
	insert := func(c Check) {
		t.Helper()
		if err := s.InsertCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	insert(Check{MonitorID: ids[0], At: finished, OK: true, LatencyMS: 100})
	insert(Check{MonitorID: ids[0], At: finished.Add(59*time.Minute + 59*time.Second), OK: true, LatencyMS: 201})
	insert(Check{MonitorID: ids[0], At: finished.Add(30 * time.Minute), LatencyMS: 900, Error: "HTTP 503"})
	insert(Check{MonitorID: ids[1], At: finished.Add(10 * time.Minute), Error: "timeout"})
	insert(Check{MonitorID: ids[0], At: current, OK: true, LatencyMS: 50})

	type row struct {
		total, ok int
		avg       sql.NullInt64
	}
	read := func(id int64, hour time.Time) (row, bool) {
		t.Helper()
		var r row
		err := s.db.QueryRowContext(ctx, `SELECT total, ok, avg_latency FROM hourly WHERE monitor_id = ? AND hour = ?`,
			id, hour.Unix()).Scan(&r.total, &r.ok, &r.avg)
		if errors.Is(err, sql.ErrNoRows) {
			return r, false
		}
		if err != nil {
			t.Fatal(err)
		}
		return r, true
	}
	tests := []struct {
		name     string
		late     *Check
		id       int64
		hour     time.Time
		want     row
		wantRows bool
	}{
		{"finished hour, average rounded", nil, ids[0], finished, row{3, 2, sql.NullInt64{Int64: 151, Valid: true}}, true},
		{"failures only, no latency", nil, ids[1], finished, row{1, 0, sql.NullInt64{}}, true},
		{"current hour has no row", nil, ids[0], current, row{}, false},
		{"late check rewrites the hour", &Check{MonitorID: ids[0], At: finished.Add(45 * time.Minute), OK: true, LatencyMS: 300}, ids[0], finished, row{4, 3, sql.NullInt64{Int64: 200, Valid: true}}, true},
	}
	for _, tc := range tests {
		if tc.late != nil {
			insert(*tc.late)
		}
		if err := s.Rollup(ctx, now); err != nil {
			t.Fatal(err)
		}
		if got, found := read(tc.id, tc.hour); found != tc.wantRows || got != tc.want {
			t.Errorf("%s: row = %+v (found %v), want %+v (found %v)", tc.name, got, found, tc.want, tc.wantRows)
		}
	}

	for at, wait := range map[time.Time]time.Duration{
		time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC):   30 * time.Second,
		time.Date(2026, 9, 14, 12, 0, 30, 0, time.UTC):  time.Hour,
		time.Date(2026, 9, 14, 12, 10, 0, 0, time.UTC):  50*time.Minute + 30*time.Second,
		time.Date(2026, 9, 14, 23, 59, 50, 0, time.UTC): 40 * time.Second,
	} {
		if got := nextRun(at); got != wait {
			t.Errorf("nextRun(%s) = %v, want %v", at.Format(time.TimeOnly), got, wait)
		}
	}
}

// TestRollupDays writes checks on today, yesterday and an older day in the
// 30-day window. The job must write both days and no row for today. After
// late checks, it must rewrite yesterday and keep the older day.
func TestRollupDays(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	m := &Monitor{Name: "API", Type: TypeHTTP, Target: "https://api.example.com", IntervalS: 60}
	if err := s.CreateMonitor(ctx, m); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	today := now.UTC().Truncate(24 * time.Hour)
	yesterday, older := today.AddDate(0, 0, -1), today.AddDate(0, 0, -5)
	next := map[time.Time]time.Duration{}
	add := func(day time.Time, n, ok int) {
		t.Helper()
		for i := 0; i < n; i++ {
			next[day] += time.Minute
			c := Check{MonitorID: m.ID, At: day.Add(time.Hour + next[day]), OK: i < ok, LatencyMS: 40}
			if err := s.InsertCheck(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
	}
	row := func(day time.Time) (DayTotals, bool) {
		t.Helper()
		var d DayTotals
		err := s.db.QueryRowContext(ctx, `SELECT total, ok FROM daily WHERE monitor_id = ? AND day = ?`,
			m.ID, day.Format("2006-01-02")).Scan(&d.Total, &d.OK)
		if errors.Is(err, sql.ErrNoRows) {
			return d, false
		}
		if err != nil {
			t.Fatal(err)
		}
		return d, true
	}
	want := func(day time.Time, total, ok int) {
		t.Helper()
		if d, found := row(day); !found || d.Total != total || d.OK != ok {
			t.Fatalf("daily row of %s = %+v (found %v), want %d checks, %d ok", day.Format("2006-01-02"), d, found, total, ok)
		}
	}

	add(older, 4, 3)
	add(yesterday, 2, 2)
	add(today, 3, 1)
	if err := s.Rollup(ctx, now); err != nil {
		t.Fatal(err)
	}
	want(older, 4, 3)
	want(yesterday, 2, 2)
	if d, found := row(today); found {
		t.Fatalf("today has a daily row %+v, want none", d)
	}

	add(older, 1, 0)
	add(yesterday, 1, 0)
	if err := s.Rollup(ctx, now); err != nil {
		t.Fatal(err)
	}
	want(older, 4, 3)
	want(yesterday, 3, 2)
}
