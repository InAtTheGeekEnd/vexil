package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestGroups(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	if got, err := s.Groups(ctx); err != nil || len(got) != 0 {
		t.Fatalf("Groups on an empty database = %+v, %v", got, err)
	}
	prod := &Group{Name: "Production"}
	staging := &Group{Name: "Staging"}
	cafe := &Group{Name: "Café"}
	for _, g := range []*Group{prod, staging, cafe} {
		if err := s.CreateGroup(ctx, g); err != nil {
			t.Fatalf("CreateGroup(%q): %v", g.Name, err)
		}
	}

	tests := []struct {
		name string
		op   func() error
		want error
	}{
		{"create with the same name", func() error { return s.CreateGroup(ctx, &Group{Name: "Production"}) }, ErrNameInUse},
		{"create with another case", func() error { return s.CreateGroup(ctx, &Group{Name: "pRODUCTION"}) }, ErrNameInUse},
		{"create with another case of a non-ASCII letter", func() error { return s.CreateGroup(ctx, &Group{Name: "CAFÉ"}) }, ErrNameInUse},
		{"rename to the name of another group", func() error { return s.RenameGroup(ctx, staging.ID, "production") }, ErrNameInUse},
		{"rename to its own name in another case", func() error { return s.RenameGroup(ctx, prod.ID, "PRODUCTION") }, nil},
		{"rename a missing group", func() error { return s.RenameGroup(ctx, 999, "Other") }, ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.op(); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}

	// Put two of three monitors in Production.
	var ids []int64
	for _, name := range []string{"a", "b", "c"} {
		m := &Monitor{Name: name, Type: TypeHTTP, Target: "https://example.com"}
		if err := s.CreateMonitor(ctx, m); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE monitors SET group_id = ? WHERE id IN (?, ?)`, prod.ID, ids[0], ids[1]); err != nil {
		t.Fatal(err)
	}

	groups, err := s.Groups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		name     string
		position int
		monitors int
	}{{"PRODUCTION", 1, 2}, {"Staging", 2, 0}, {"Café", 3, 0}}
	if len(groups) != len(want) {
		t.Fatalf("Groups = %+v", groups)
	}
	for i, w := range want {
		if g := groups[i]; g.Name != w.name || g.Position != w.position || g.Monitors != w.monitors || g.CreatedAt.IsZero() {
			t.Errorf("group %d = %+v, want %+v", i, g, w)
		}
	}
	if g, err := s.Group(ctx, prod.ID); err != nil || g.Monitors != 2 {
		t.Fatalf("Group = %+v, %v", g, err)
	}

	// Deleting a group keeps its monitors and moves them out of the group.
	if err := s.DeleteGroup(ctx, prod.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Group(ctx, prod.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteGroup(ctx, prod.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}
	if monitors, _ := s.Monitors(ctx); len(monitors) != 3 {
		t.Fatalf("monitors after delete = %d, want 3", len(monitors))
	}
	var grouped int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM monitors WHERE group_id IS NOT NULL`).Scan(&grouped); err != nil || grouped != 0 {
		t.Fatalf("grouped monitors after delete = %d, %v", grouped, err)
	}
}

// TestGroupsMigration upgrades a database from before groups. Every monitor
// must stay as it was and be in no group.
func TestGroupsMigration(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "vexil.db")))
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`}
	for _, name := range []string{"migrations/001_init.sql", "migrations/002_notifications.sql"} {
		b, err := migrationFS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		stmts = append(stmts, string(b))
	}
	stmts = append(stmts,
		`INSERT INTO schema_migrations (version) VALUES (1), (2)`,
		`INSERT INTO monitors (name, type, target, interval_s, public, position, created_at)
			VALUES ('Site', 'http', 'https://example.com', 30, 1, 1, 1700000000)`)
	for _, q := range stmts {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()

	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	monitors, err := s.Monitors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(monitors) != 1 {
		t.Fatalf("monitors = %+v", monitors)
	}
	if m := monitors[0]; m.Name != "Site" || m.IntervalS != 30 || !m.Public || m.Position != 1 {
		t.Fatalf("monitor changed: %+v", m)
	}
	var grouped int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM monitors WHERE group_id IS NOT NULL`).Scan(&grouped); err != nil || grouped != 0 {
		t.Fatalf("grouped monitors = %d, %v", grouped, err)
	}
	if groups, err := s.Groups(ctx); err != nil || len(groups) != 0 {
		t.Fatalf("groups = %+v, %v", groups, err)
	}
}
