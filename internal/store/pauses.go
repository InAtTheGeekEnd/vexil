package store

import (
	"context"
	"database/sql"
	"time"
)

// Pause is one period a monitor was paused.
type Pause struct {
	MonitorID int64
	StartedAt time.Time
	EndedAt   time.Time // zero while the pause lasts
}

// Open reports whether the pause has not ended.
func (p Pause) Open() bool { return p.EndedAt.IsZero() }

// SetPaused pauses or resumes a monitor at the given time and records the
// pause. A pause of a monitor that is already paused, or a resume of one
// that runs, changes nothing.
func (s *Store) SetPaused(ctx context.Context, id int64, paused bool, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE monitors SET paused = ? WHERE id = ?`, paused, id)
	if err != nil {
		return err
	}
	if err := affected(res); err != nil {
		return err
	}
	if paused {
		_, err = tx.ExecContext(ctx, `INSERT INTO pauses (monitor_id, started_at) SELECT ?, ?
			WHERE NOT EXISTS (SELECT 1 FROM pauses WHERE monitor_id = ? AND ended_at IS NULL)`, id, at.Unix(), id)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE pauses SET ended_at = ? WHERE monitor_id = ? AND ended_at IS NULL`, at.Unix(), id)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// pausesQuery selects the pauses that were open at any time since ?1.
const pausesQuery = `SELECT monitor_id, started_at, ended_at FROM pauses
	WHERE (ended_at IS NULL OR ended_at >= ?1)`

// PausesSince returns the pauses of every monitor that were open at any
// time since the given time, oldest first, by monitor id.
func (s *Store) PausesSince(ctx context.Context, since time.Time) (map[int64][]Pause, error) {
	rows, err := s.db.QueryContext(ctx, pausesQuery+` ORDER BY monitor_id, started_at`, since.Unix())
	if err != nil {
		return nil, err
	}
	all, err := scanPauses(rows)
	if err != nil {
		return nil, err
	}
	out := map[int64][]Pause{}
	for _, p := range all {
		out[p.MonitorID] = append(out[p.MonitorID], p)
	}
	return out, nil
}

// MonitorPauses returns the pauses of one monitor that were open at any
// time since the given time, oldest first.
func (s *Store) MonitorPauses(ctx context.Context, monitorID int64, since time.Time) ([]Pause, error) {
	rows, err := s.db.QueryContext(ctx, pausesQuery+` AND monitor_id = ?2 ORDER BY started_at`, since.Unix(), monitorID)
	if err != nil {
		return nil, err
	}
	return scanPauses(rows)
}

func scanPauses(rows *sql.Rows) ([]Pause, error) {
	defer rows.Close()
	var out []Pause
	for rows.Next() {
		var p Pause
		var started int64
		var ended sql.NullInt64
		if err := rows.Scan(&p.MonitorID, &started, &ended); err != nil {
			return nil, err
		}
		p.StartedAt = time.Unix(started, 0)
		if ended.Valid {
			p.EndedAt = time.Unix(ended.Int64, 0)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
