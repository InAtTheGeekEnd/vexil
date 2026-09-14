package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Group is one row of the monitor_groups table.
type Group struct {
	ID        int64
	Name      string
	Position  int
	CreatedAt time.Time
	Monitors  int // the number of monitors in the group; read only
}

// ErrNameInUse reports that another group has the same name, ignoring case.
var ErrNameInUse = errors.New("name in use")

const groupQuery = `SELECT g.id, g.name, g.position, g.created_at, COUNT(m.id)
	FROM monitor_groups g LEFT JOIN monitors m ON m.group_id = g.id`

func scanGroup(row interface{ Scan(...any) error }) (Group, error) {
	var g Group
	var created int64
	err := row.Scan(&g.ID, &g.Name, &g.Position, &created, &g.Monitors)
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	g.CreatedAt = time.Unix(created, 0)
	return g, err
}

// CreateGroup inserts g after the other groups and fills in ID and
// CreatedAt. It returns ErrNameInUse when the name is taken.
func (s *Store) CreateGroup(ctx context.Context, g *Group) error {
	if g.CreatedAt.IsZero() {
		g.CreatedAt = time.Now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := groupNameInUse(ctx, tx, g.Name, 0); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO monitor_groups (name, position, created_at)
		VALUES (?, (SELECT COALESCE(MAX(position), 0) + 1 FROM monitor_groups), ?)`,
		g.Name, g.CreatedAt.Unix())
	if err != nil {
		return err
	}
	if g.ID, err = res.LastInsertId(); err != nil {
		return err
	}
	return tx.Commit()
}

// RenameGroup changes the name of a group. It returns ErrNameInUse when
// another group has the name.
func (s *Store) RenameGroup(ctx context.Context, id int64, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := groupNameInUse(ctx, tx, name, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE monitor_groups SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return err
	}
	if err := affected(res); err != nil {
		return err
	}
	return tx.Commit()
}

// groupNameInUse returns ErrNameInUse when a group other than id has name.
// It compares in Go because SQLite ignores case only for ASCII letters.
func groupNameInUse(ctx context.Context, tx *sql.Tx, name string, id int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, name FROM monitor_groups`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var otherID int64
		var other string
		if err := rows.Scan(&otherID, &other); err != nil {
			return err
		}
		if otherID != id && strings.EqualFold(other, name) {
			return ErrNameInUse
		}
	}
	return rows.Err()
}

// DeleteGroup removes a group. Its monitors move out of the group. No
// monitor is deleted.
func (s *Store) DeleteGroup(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE monitors SET group_id = NULL WHERE group_id = ?`, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM monitor_groups WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if err := affected(res); err != nil {
		return err
	}
	return tx.Commit()
}

// Group returns one group or ErrNotFound.
func (s *Store) Group(ctx context.Context, id int64) (Group, error) {
	return scanGroup(s.db.QueryRowContext(ctx, groupQuery+` WHERE g.id = ? GROUP BY g.id`, id))
}

// Groups returns every group in display order.
func (s *Store) Groups(ctx context.Context) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, groupQuery+` GROUP BY g.id ORDER BY g.position, g.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
