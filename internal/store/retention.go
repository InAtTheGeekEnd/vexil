package store

import (
	"context"
	"log/slog"
	"time"
)

// RawRetention is how long raw check rows are kept (SPEC.md section 5).
const RawRetention = 30 * 24 * time.Hour

// retentionBatch is the number of check rows deleted per statement. Small
// batches keep every statement short, so the check writer never waits
// long for the database.
const retentionBatch = 500

// checksAvgLatency is the average latency of the successful checks in a
// group of check rows, rounded to a whole millisecond.
const checksAvgLatency = `CAST(ROUND(AVG(CASE WHEN ok = 1 THEN latency_ms END)) AS INTEGER)`

// Retain deletes the raw checks of the whole UTC days that ended before
// RawRetention ago, in batches of batch rows, one monitor at a time so
// every statement searches the index of the checks. Then the hourly rows
// before the cutoff go. It returns the number of deleted check rows.
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
			res, err := s.db.ExecContext(ctx, `
				DELETE FROM checks WHERE rowid IN (
					SELECT rowid FROM checks WHERE monitor_id = ? AND at < ? LIMIT ?)`,
				id, cutoff.Unix(), batch)
			if err != nil {
				return deleted, err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return deleted, err
			}
			deleted += n
			if n < int64(batch) {
				break
			}
		}
	}
	// The hourly rows before the cutoff go after the checks, in one statement
	// that searches the primary key of every monitor. It also removes rows
	// whose checks an earlier run deleted before it stopped.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM hourly WHERE monitor_id IN (SELECT id FROM monitors) AND hour < ?`,
		cutoff.Unix()); err != nil {
		return deleted, err
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

// Rollup writes the hourly rows of recent hours.
func (s *Store) Rollup(ctx context.Context, now time.Time) error {
	return s.rollupHours(ctx, now)
}

// rollupHours writes the hourly rows of the finished UTC hours in the 30
// days before now. Oldest first, it fills each hour that has no row yet, as
// after a downtime. Then it rewrites the last finished hour, as checks that
// end after an earlier run can come in late. The current hour gets no row:
// the pages read it from the checks.
func (s *Store) rollupHours(ctx context.Context, now time.Time) error {
	last := now.UTC().Truncate(time.Hour).Add(-time.Hour)
	for hour := now.UTC().Add(-RawRetention).Truncate(time.Hour); hour.Before(last); hour = hour.Add(time.Hour) {
		if err := s.rollupHour(ctx, hour, false); err != nil {
			return err
		}
	}
	return s.rollupHour(ctx, last, true)
}

// rollupHour writes the hourly rows of one UTC hour for every monitor with
// checks in it, in one statement. With replace it rewrites the existing
// rows. Without replace it writes only the missing rows, so a filled hour
// costs one primary key lookup per monitor.
func (s *Store) rollupHour(ctx context.Context, hour time.Time, replace bool) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO hourly (monitor_id, hour, ok, avg_latency)
		SELECT monitor_id, ?1, SUM(ok), `+checksAvgLatency+`
		FROM checks
		WHERE monitor_id IN (SELECT id FROM monitors WHERE ?3 OR NOT EXISTS (
				SELECT 1 FROM hourly WHERE hourly.monitor_id = monitors.id AND hourly.hour = ?1))
			AND at >= ?1 AND at < ?2
		GROUP BY monitor_id
		ON CONFLICT(monitor_id, hour) DO UPDATE SET ok = excluded.ok, avg_latency = excluded.avg_latency`,
		hour.Unix(), hour.Add(time.Hour).Unix(), replace)
	return err
}

// nextRun returns the wait until the next run of the hourly job: 30 seconds
// after the next full UTC hour, so the hour that ended gets its rows soon.
// A check takes at most 10 seconds, so the checks of the hour are in by
// then.
func nextRun(now time.Time) time.Duration {
	next := now.UTC().Truncate(time.Hour).Add(30 * time.Second)
	if !next.After(now) {
		next = next.Add(time.Hour)
	}
	return next.Sub(now)
}

// RunRetention deletes expired sessions, writes the hourly rows of recent
// hours and runs Retain, at once and then 30 seconds after
// every full UTC hour, until ctx ends. The returned channel closes when the
// job has stopped.
func (s *Store) RunRetention(ctx context.Context, log *slog.Logger) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if err := s.DeleteExpiredSessions(ctx, time.Now()); err != nil && ctx.Err() == nil {
				log.Error("delete expired sessions", "err", err)
			}
			if err := s.Rollup(ctx, time.Now()); err != nil && ctx.Err() == nil {
				log.Error("rollup failed", "err", err)
			}
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
			case <-time.After(nextRun(time.Now())):
			}
		}
	}()
	return done
}
