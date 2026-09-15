package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// hourRow is one row of the hourly table.
type hourRow struct {
	ok  int
	avg sql.NullInt64
}

// readHour returns the hourly row of a monitor and hour, and false when
// there is none.
func readHour(t *testing.T, s *Store, id int64, hour time.Time) (hourRow, bool) {
	t.Helper()
	var r hourRow
	err := s.db.QueryRowContext(context.Background(), `SELECT ok, avg_latency FROM hourly WHERE monitor_id = ? AND hour = ?`,
		id, hour.Unix()).Scan(&r.ok, &r.avg)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return r, true
}

// TestRollupHoursFillsMissing gives monitor A checks in hours of the 30-day
// window that have no rows, as after a downtime, and in the hour before the
// window. Monitor B already has a row for one of the hours. The job must
// write the missing rows, keep the row of B, write nothing before the
// window, and keep a filled row when a late check comes in.
func TestRollupHoursFillsMissing(t *testing.T) {
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
	old := now.Truncate(time.Hour).AddDate(0, 0, -3)
	first := now.Add(-RawRetention).Truncate(time.Hour)
	before := first.Add(-time.Hour)
	insert := func(c Check) {
		t.Helper()
		if err := s.InsertCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	insert(Check{MonitorID: ids[0], At: old.Add(time.Minute), OK: true, LatencyMS: 100})
	insert(Check{MonitorID: ids[0], At: old.Add(2 * time.Minute), OK: true, LatencyMS: 200})
	insert(Check{MonitorID: ids[1], At: old.Add(5 * time.Minute), Error: "timeout"})
	insert(Check{MonitorID: ids[0], At: first.Add(59 * time.Minute), OK: true, LatencyMS: 80})
	insert(Check{MonitorID: ids[0], At: before.Add(30 * time.Minute), OK: true, LatencyMS: 80})
	if _, err := s.db.ExecContext(ctx, `INSERT INTO hourly (monitor_id, hour, ok, avg_latency) VALUES (?, ?, 9, 1)`,
		ids[1], old.Unix()); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		late     *Check
		id       int64
		hour     time.Time
		want     hourRow
		wantRows bool
	}{
		{"missing hour is filled", nil, ids[0], old, hourRow{2, sql.NullInt64{Int64: 150, Valid: true}}, true},
		{"existing row is kept", nil, ids[1], old, hourRow{9, sql.NullInt64{Int64: 1, Valid: true}}, true},
		{"first hour of the window", nil, ids[0], first, hourRow{1, sql.NullInt64{Int64: 80, Valid: true}}, true},
		{"hour before the window", nil, ids[0], before, hourRow{}, false},
		{"late check keeps the filled row", &Check{MonitorID: ids[0], At: old.Add(3 * time.Minute), Error: "HTTP 503"}, ids[0], old, hourRow{2, sql.NullInt64{Int64: 150, Valid: true}}, true},
	}
	for _, tc := range tests {
		if tc.late != nil {
			insert(*tc.late)
		}
		if err := s.Rollup(ctx, now); err != nil {
			t.Fatal(err)
		}
		if got, found := readHour(t, s, tc.id, tc.hour); found != tc.wantRows || got != tc.want {
			t.Errorf("%s: row = %+v (found %v), want %+v (found %v)", tc.name, got, found, tc.want, tc.wantRows)
		}
	}
}

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

	tests := []struct {
		name     string
		late     *Check
		id       int64
		hour     time.Time
		want     hourRow
		wantRows bool
	}{
		{"finished hour, average rounded", nil, ids[0], finished, hourRow{2, sql.NullInt64{Int64: 151, Valid: true}}, true},
		{"failures only, no latency", nil, ids[1], finished, hourRow{0, sql.NullInt64{}}, true},
		{"current hour has no row", nil, ids[0], current, hourRow{}, false},
		{"late check rewrites the hour", &Check{MonitorID: ids[0], At: finished.Add(45 * time.Minute), OK: true, LatencyMS: 300}, ids[0], finished, hourRow{3, sql.NullInt64{Int64: 200, Valid: true}}, true},
	}
	for _, tc := range tests {
		if tc.late != nil {
			insert(*tc.late)
		}
		if err := s.Rollup(ctx, now); err != nil {
			t.Fatal(err)
		}
		if got, found := readHour(t, s, tc.id, tc.hour); found != tc.wantRows || got != tc.want {
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
