package store

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// seedChecks inserts one check per minute for n minutes from start.
func seedChecks(t *testing.T, s *Store, id int64, start time.Time, n int, ok bool) {
	t.Helper()
	for i := 0; i < n; i++ {
		c := Check{MonitorID: id, At: start.Add(time.Duration(i) * time.Minute), OK: ok, LatencyMS: 100}
		if err := s.InsertCheck(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
}

func countChecks(t *testing.T, s *Store, id int64) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM checks WHERE monitor_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRetainDeletesInBatches(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	old := &Monitor{Name: "Old", Type: TypeHTTP, Target: "https://a.example", IntervalS: 60}
	fresh := &Monitor{Name: "Fresh", Type: TypeHTTP, Target: "https://b.example", IntervalS: 60}
	for _, m := range []*Monitor{old, fresh} {
		if err := s.CreateMonitor(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	// Two old days with 1440 checks each, one day right at the cutoff, and
	// recent checks that must stay.
	seedChecks(t, s, old.ID, now.AddDate(0, 0, -45).Truncate(24*time.Hour), 1440, true)
	seedChecks(t, s, old.ID, now.AddDate(0, 0, -44).Truncate(24*time.Hour), 1440, true)
	cutoffDay := now.AddDate(0, 0, -30).Truncate(24 * time.Hour)
	seedChecks(t, s, old.ID, cutoffDay, 60, true)
	seedChecks(t, s, old.ID, now.Add(-2*time.Hour), 60, true)
	seedChecks(t, s, fresh.ID, now.Add(-time.Hour), 30, false)

	tests := []struct {
		name  string
		batch int
		want  int64
	}{
		{"first run deletes both old days", 100, 2880},
		{"second run has nothing left", 100, 0},
	}
	for _, tc := range tests {
		n, err := s.Retain(ctx, now, tc.batch)
		if err != nil || n != tc.want {
			t.Fatalf("%s: Retain = %d, %v; want %d", tc.name, n, err, tc.want)
		}
	}
	if got := countChecks(t, s, old.ID); got != 120 {
		t.Fatalf("old monitor has %d checks left, want 120", got)
	}
	if got := countChecks(t, s, fresh.ID); got != 30 {
		t.Fatalf("fresh monitor has %d checks left, want 30", got)
	}

	// A cancelled context stops between batches without an error in the
	// data: the rest goes next time.
	seedChecks(t, s, old.ID, now.AddDate(0, 0, -40), 300, true)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Retain(cctx, now, 100); err == nil {
		t.Fatal("Retain ignored the cancelled context")
	}
	n, err := s.Retain(ctx, now, 100)
	if err != nil || n != 300 {
		t.Fatalf("Retain after cancel = %d, %v", n, err)
	}
}

// TestRetainDeletesHourly runs the job and then Retain. The hourly rows
// before the cutoff go with the checks, also a row of a monitor that has no
// checks left, as after a run that stopped between the two deletes. The
// hours of the cutoff day and recent hours stay.
func TestRetainDeletesHourly(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	a := &Monitor{Name: "A", Type: TypeHTTP, Target: "https://a.example", IntervalS: 60}
	b := &Monitor{Name: "B", Type: TypeHTTP, Target: "https://b.example", IntervalS: 60}
	for _, m := range []*Monitor{a, b} {
		if err := s.CreateMonitor(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	expired := now.AddDate(0, 0, -45).Truncate(24 * time.Hour)
	cutoffDay := now.AddDate(0, 0, -30).Truncate(24 * time.Hour)
	seedChecks(t, s, a.ID, expired.Add(time.Hour), 60, true)
	seedChecks(t, s, a.ID, cutoffDay.Add(13*time.Hour), 60, true)
	seedChecks(t, s, a.ID, now.Add(-2*time.Hour), 60, true)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO hourly (monitor_id, hour, ok) VALUES (?, ?, 60)`,
		b.ID, expired.Add(5*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.Rollup(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Retain(ctx, now, retentionBatch); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		id    int64
		hour  time.Time
		found bool
	}{
		{"expired hour", a.ID, expired.Add(time.Hour), false},
		{"expired row without checks", b.ID, expired.Add(5 * time.Hour), false},
		{"hour on the cutoff day", a.ID, cutoffDay.Add(13 * time.Hour), true},
		{"recent hour", a.ID, now.Add(-2 * time.Hour), true},
	}
	for _, tc := range tests {
		if _, found := readHour(t, s, tc.id, tc.hour); found != tc.found {
			t.Errorf("%s: hourly row found %v, want %v", tc.name, found, tc.found)
		}
	}
}

func TestRunRetentionStops(t *testing.T) {
	s := openTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := s.RunRetention(ctx, discardLogger())
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunRetention did not stop")
	}
}
