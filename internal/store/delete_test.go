package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// countRows returns the rows of a monitor in the checks, daily and
// incidents tables.
func countRows(t *testing.T, s *Store, id int64) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range []string{"checks", "daily", "incidents"} {
		var n int
		if err := s.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table+` WHERE monitor_id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[table] = n
	}
	return out
}

// TestDeleteMonitorRemovesHistory deletes a monitor that has checks, a
// daily row and an incident. None of its rows may be left, and another
// monitor keeps all of its rows.
func TestDeleteMonitorRemovesHistory(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	now := time.Now().Truncate(time.Second)
	var ids []int64
	for _, name := range []string{"Gone", "Kept"} {
		m := &Monitor{Name: name, Type: TypeHTTP, Target: "https://example.com", IntervalS: 60}
		if err := s.CreateMonitor(ctx, m); err != nil {
			t.Fatal(err)
		}
		if err := s.InsertCheck(ctx, Check{MonitorID: m.ID, At: now, Error: "HTTP 503"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.OpenIncident(ctx, m.ID, now, "HTTP 503"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO daily (monitor_id, day, total, ok) VALUES (?, '2026-09-01', 10, 9)`, m.ID); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}

	if err := s.DeleteMonitor(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Monitor(ctx, ids[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted monitor: err = %v, want ErrNotFound", err)
	}
	for i, want := range []int{0, 1} {
		for table, n := range countRows(t, s, ids[i]) {
			if n != want {
				t.Errorf("monitor %d: %d %s rows, want %d", ids[i], n, table, want)
			}
		}
	}
}
