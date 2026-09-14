package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// TestRollupDays writes checks on today, yesterday and an older day in the
// 30-day window. The job must write both days and no row for today. After
// late checks, it must rewrite yesterday and keep the older day. The job
// runs again after an hour, or 30 seconds after the next UTC midnight.
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
	if err := s.RollupDays(ctx, now); err != nil {
		t.Fatal(err)
	}
	want(older, 4, 3)
	want(yesterday, 2, 2)
	if d, found := row(today); found {
		t.Fatalf("today has a daily row %+v, want none", d)
	}

	add(older, 1, 0)
	add(yesterday, 1, 0)
	if err := s.RollupDays(ctx, now); err != nil {
		t.Fatal(err)
	}
	want(older, 4, 3)
	want(yesterday, 3, 2)

	for at, wait := range map[time.Time]time.Duration{
		time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC):  time.Hour,
		time.Date(2026, 9, 14, 23, 50, 0, 0, time.UTC): 10*time.Minute + 30*time.Second,
	} {
		if got := nextRun(at); got != wait {
			t.Errorf("nextRun(%s) = %v, want %v", at.Format(time.TimeOnly), got, wait)
		}
	}
}
