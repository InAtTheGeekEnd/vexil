package store

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

// RawRetention is how long raw check rows are kept (SPEC.md section 5).
const RawRetention = 30 * 24 * time.Hour

// retentionBatch is the number of check rows deleted per statement. Small
// batches keep every statement short, so the check writer never waits
// long for the database.
const retentionBatch = 500

// Retain rolls raw checks older than RawRetention into the daily table and
// deletes them. It works on whole UTC days that ended before the cutoff,
// oldest first, one day of one monitor at a time. The daily row is
// written first. Then the checks of that day go in batches of batch rows.
// It returns the number of deleted rows.
func (s *Store) Retain(ctx context.Context, now time.Time, batch int) (int64, error) {
	c := now.UTC().Add(-RawRetention)
	cutoff := time.Date(c.Year(), c.Month(), c.Day(), 0, 0, 0, 0, time.UTC)

	ids, err := s.monitorsWithChecks(ctx)
	if err != nil {
		return 0, err
	}
	var deleted int64
	for _, id := range ids {
		for {
			if err := ctx.Err(); err != nil {
				return deleted, err
			}
			day, ok, err := s.oldestDay(ctx, id)
			if err != nil {
				return deleted, err
			}
			if !ok || !day.Before(cutoff) {
				break
			}
			if err := s.rollupDay(ctx, id, day); err != nil {
				return deleted, err
			}
			n, err := s.deleteDay(ctx, id, day, batch)
			deleted += n
			if err != nil {
				return deleted, err
			}
		}
	}
	return deleted, nil
}

func (s *Store) monitorsWithChecks(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT monitor_id FROM checks ORDER BY monitor_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// oldestDay returns the UTC day of the oldest check of a monitor.
func (s *Store) oldestDay(ctx context.Context, monitorID int64) (time.Time, bool, error) {
	var min sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(at) FROM checks WHERE monitor_id = ?`, monitorID).Scan(&min); err != nil {
		return time.Time{}, false, err
	}
	if !min.Valid {
		return time.Time{}, false, nil
	}
	t := time.Unix(min.Int64, 0).UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), true, nil
}

// rollupDay writes the daily row for one monitor and day from its raw
// checks. An existing row is kept: it was written by an earlier run that
// did not finish deleting, and it is complete while the checks may not be.
func (s *Store) rollupDay(ctx context.Context, monitorID int64, day time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO daily (monitor_id, day, total, ok, avg_latency)
		SELECT ?, ?, COUNT(*), COALESCE(SUM(ok), 0), AVG(CASE WHEN ok = 1 THEN latency_ms END)
		FROM checks WHERE monitor_id = ? AND at >= ? AND at < ?
		ON CONFLICT(monitor_id, day) DO NOTHING`,
		monitorID, day.Format("2006-01-02"), monitorID, day.Unix(), day.Add(24*time.Hour).Unix())
	return err
}

// deleteDay removes the checks of one monitor and day in batches.
func (s *Store) deleteDay(ctx context.Context, monitorID int64, day time.Time, batch int) (int64, error) {
	var deleted int64
	for {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		res, err := s.db.ExecContext(ctx, `
			DELETE FROM checks WHERE rowid IN (
				SELECT rowid FROM checks WHERE monitor_id = ? AND at >= ? AND at < ? LIMIT ?)`,
			monitorID, day.Unix(), day.Add(24*time.Hour).Unix(), batch)
		if err != nil {
			return deleted, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return deleted, err
		}
		deleted += n
		if n < int64(batch) {
			return deleted, nil
		}
	}
}

// RunRetention runs Retain at once and then every hour until ctx ends. The
// returned channel closes when the job has stopped.
func (s *Store) RunRetention(ctx context.Context, log *slog.Logger) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			n, err := s.Retain(ctx, time.Now(), retentionBatch)
			switch {
			case ctx.Err() != nil:
				return
			case err != nil:
				log.Error("retention failed", "err", err)
			case n > 0:
				log.Info("retention done", "deleted", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return done
}
