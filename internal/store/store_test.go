package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		s, err := Open(ctx, dir)
		if err != nil {
			t.Fatalf("Open #%d: %v", i+1, err)
		}
		if err := s.Ping(ctx); err != nil {
			t.Fatalf("Ping #%d: %v", i+1, err)
		}
		s.Close()
	}
}

func TestMigrationsCreateTables(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	tables := []string{"monitors", "monitor_groups", "checks", "hourly", "daily", "incidents", "channels", "sessions", "settings", "schema_migrations"}
	for _, tbl := range tables {
		t.Run(tbl, func(t *testing.T) {
			var n int
			err := s.db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, tbl).Scan(&n)
			if err != nil || n != 1 {
				t.Fatalf("table %s: n=%d err=%v", tbl, n, err)
			}
		})
	}
}

func TestMigrationVersion(t *testing.T) {
	tests := []struct {
		name    string
		want    int
		wantErr bool
	}{
		{"migrations/001_init.sql", 1, false},
		{"migrations/012_add_thing.sql", 12, false},
		{"migrations/init.sql", 0, true},
		{"migrations/x_init.sql", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := migrationVersion(tt.name)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSettings(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	if _, err := s.GetSetting(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing key: err = %v, want ErrNotFound", err)
	}
	if err := s.SetSetting(ctx, "k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "k", "v2"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSetting(ctx, "k")
	if err != nil || got != "v2" {
		t.Fatalf("GetSetting = %q, %v; want v2", got, err)
	}
}

func TestPasswordAndSessions(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)

	has, err := s.HasPassword(ctx)
	if err != nil || has {
		t.Fatalf("HasPassword on empty db = %v, %v; want false", has, err)
	}

	if err := s.CreateSession(ctx, "live", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, "old", now.Add(-2*time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		hash string
		want bool
	}{
		{"live", true},
		{"old", false},
		{"unknown", false},
	}
	for _, tt := range tests {
		ok, err := s.SessionValid(ctx, tt.hash, now)
		if err != nil || ok != tt.want {
			t.Fatalf("SessionValid(%q) = %v, %v; want %v", tt.hash, ok, err, tt.want)
		}
	}

	if err := s.DeleteExpiredSessions(ctx, now); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.SessionValid(ctx, "live", now); !ok {
		t.Fatal("live session removed by DeleteExpiredSessions")
	}

	// Setting a password removes every session.
	if err := s.SetPasswordHash(ctx, "hash", ""); err != nil {
		t.Fatal(err)
	}
	has, err = s.HasPassword(ctx)
	if err != nil || !has {
		t.Fatalf("HasPassword after set = %v, %v; want true", has, err)
	}
	if h, _ := s.PasswordHash(ctx); h != "hash" {
		t.Fatalf("PasswordHash = %q", h)
	}
	if ok, _ := s.SessionValid(ctx, "live", now); ok {
		t.Fatal("session survived SetPasswordHash")
	}

	if err := s.CreateSession(ctx, "a", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.SessionValid(ctx, "a", now); ok {
		t.Fatal("session survived DeleteSession")
	}
}
