package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBackup writes rows, backs the database up while it is open and opens
// the copy. The rows must be in the copy, also those still in the WAL, and
// a second backup to the same file must fail.
func TestBackup(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	m := &Monitor{Name: "API", Type: TypeHTTP, Target: "https://api.example.com", IntervalS: 60}
	if err := s.CreateMonitor(ctx, m); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).Truncate(time.Second)
	if err := s.InsertCheck(ctx, Check{MonitorID: m.ID, At: at, OK: true, LatencyMS: 42}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "vexil.db")
	if err := s.Backup(ctx, path); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	// The source is still open and usable.
	if err := s.InsertCheck(ctx, Check{MonitorID: m.ID, At: at.Add(time.Second), OK: false, Error: "HTTP 503"}); err != nil {
		t.Fatal(err)
	}

	copied, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("open the backup: %v", err)
	}
	defer copied.Close()
	got, err := copied.Monitor(ctx, m.ID)
	if err != nil || got.Name != "API" || got.Target != m.Target {
		t.Fatalf("monitor in the backup = %+v, %v", got, err)
	}
	checks, err := copied.RecentChecks(ctx, m.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || !checks[0].OK || checks[0].LatencyMS != 42 {
		t.Fatalf("checks in the backup = %+v, want the one check from before the backup", checks)
	}

	err = s.Backup(ctx, path)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second backup to the same file: err = %v, want already exists", err)
	}
}
