package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// countRows returns the rows of a monitor in the checks, hourly, daily and
// incidents tables.
func countRows(t *testing.T, s *Store, id int64) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range []string{"checks", "hourly", "daily", "incidents"} {
		var n int
		if err := s.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table+` WHERE monitor_id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[table] = n
	}
	return out
}

// monitorWithHistory creates a monitor with one check, one hourly row, one
// daily row and one incident.
func monitorWithHistory(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
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
	if _, err := s.db.ExecContext(ctx, `INSERT INTO daily (monitor_id, day, avg_latency) VALUES (?, '2026-09-01', 90)`, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO hourly (monitor_id, hour, ok, avg_latency) VALUES (?, ?, 59, 80)`,
		m.ID, now.Truncate(time.Hour).Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	return m.ID
}

// TestMonitorIDsAreNotReused deletes the monitor with the highest id and
// adds a new one. The new id must be higher: ids leave the database in badge
// URLs, alert links and webhook payloads, so a reused id would show another
// service under an old badge.
func TestMonitorIDsAreNotReused(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	monitorWithHistory(t, s, "A")
	last := monitorWithHistory(t, s, "B")
	if err := s.DeleteMonitor(ctx, last); err != nil {
		t.Fatal(err)
	}
	m := &Monitor{Name: "C", Type: TypeHTTP, Target: "https://example.com", IntervalS: 60}
	if err := s.CreateMonitor(ctx, m); err != nil {
		t.Fatal(err)
	}
	if m.ID <= last {
		t.Fatalf("new monitor id = %d, want more than the deleted %d", m.ID, last)
	}
}

// TestDeleteCascades deletes a monitor with plain SQL, not DeleteMonitor.
// The foreign keys must remove its checks, hourly rows, daily rows and
// incidents, and a check for a monitor that does not exist must be refused.
func TestDeleteCascades(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	var on int
	if err := s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&on); err != nil || on != 1 {
		t.Fatalf("PRAGMA foreign_keys = %d, %v; want 1", on, err)
	}
	id := monitorWithHistory(t, s, "Gone")
	if _, err := s.db.ExecContext(ctx, `DELETE FROM monitors WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	for table, n := range countRows(t, s, id) {
		if n != 0 {
			t.Errorf("%d %s rows left after the delete, want 0", n, table)
		}
	}
	if err := s.InsertCheck(ctx, Check{MonitorID: id, At: time.Now(), OK: true}); err == nil {
		t.Fatal("a check for a deleted monitor was stored")
	}
}

// TestDeleteMonitorRemovesHistory deletes a monitor that has checks, an
// hourly row, a daily row and an incident. None of its rows may be left, and
// another monitor keeps all of its rows.
func TestDeleteMonitorRemovesHistory(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	ids := []int64{monitorWithHistory(t, s, "Gone"), monitorWithHistory(t, s, "Kept")}

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
