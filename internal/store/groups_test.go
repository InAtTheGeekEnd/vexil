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

func TestSaveLayout(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	a, b := &Group{Name: "A"}, &Group{Name: "B"}
	for _, g := range []*Group{a, b} {
		if err := s.CreateGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	var m [5]int64 // five monitors in no group, at positions 1 to 5
	for i := range m {
		mon := &Monitor{Name: "m", Type: TypeHTTP, Target: "https://example.com"}
		if err := s.CreateMonitor(ctx, mon); err != nil {
			t.Fatal(err)
		}
		m[i] = mon.ID
	}

	type place struct {
		group int64
		pos   int
	}
	// The steps run in order. Each one starts from the layout of the one
	// before.
	steps := []struct {
		name       string
		groups     []int64
		monitors   []Placement
		want       [5]place
		wantGroups []string
	}{
		{
			name:       "fill the groups and order them",
			groups:     []int64{b.ID, a.ID},
			monitors:   []Placement{{m[2], a.ID}, {m[0], a.ID}, {m[1], b.ID}, {m[3], 0}, {m[4], 0}},
			want:       [5]place{{a.ID, 2}, {b.ID, 1}, {a.ID, 1}, {0, 1}, {0, 2}},
			wantGroups: []string{"B", "A"},
		},
		{
			name:       "a missing monitor keeps its group and position, and the others skip it",
			groups:     []int64{a.ID, b.ID},
			monitors:   []Placement{{m[2], a.ID}, {m[1], a.ID}, {m[4], 0}, {m[3], 0}},
			want:       [5]place{{a.ID, 2}, {a.ID, 3}, {a.ID, 1}, {0, 2}, {0, 1}},
			wantGroups: []string{"A", "B"},
		},
		{
			name:       "an unknown group means no group, and groups not listed keep their order",
			monitors:   []Placement{{m[2], 999}, {m[1], a.ID}, {m[4], 0}, {m[3], 0}},
			want:       [5]place{{a.ID, 2}, {a.ID, 1}, {0, 1}, {0, 3}, {0, 2}},
			wantGroups: []string{"A", "B"},
		},
	}
	for _, tt := range steps {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.SaveLayout(ctx, tt.groups, tt.monitors); err != nil {
				t.Fatal(err)
			}
			for i, w := range tt.want {
				got, err := s.Monitor(ctx, m[i])
				if err != nil || got.GroupID != w.group || got.Position != w.pos {
					t.Errorf("monitor %d = group %d position %d (%v), want group %d position %d", i, got.GroupID, got.Position, err, w.group, w.pos)
				}
			}
			groups, err := s.Groups(ctx)
			if err != nil || len(groups) != len(tt.wantGroups) {
				t.Fatalf("groups = %+v, %v", groups, err)
			}
			for i, g := range groups {
				if g.Name != tt.wantGroups[i] {
					t.Errorf("group %d = %q, want the order %v", i, g.Name, tt.wantGroups)
				}
			}
		})
	}
}
