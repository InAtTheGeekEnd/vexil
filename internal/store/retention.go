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

// checksAvgLatency is the average latency of the successful checks in a
// group of check rows, rounded to a whole millisecond.
const checksAvgLatency = `CAST(ROUND(AVG(CASE WHEN ok = 1 THEN latency_ms END)) AS INTEGER)`

// hourlyAvgLatency is the same average for a group of hourly rows: the
// average of each hour, weighted by the successful checks of the hour.
const hourlyAvgLatency = `CAST(ROUND(SUM(avg_latency * ok) * 1.0 / SUM(CASE WHEN avg_latency IS NOT NULL THEN ok END)) AS INTEGER)`

// Retain rolls raw checks older than RawRetention into the hourly and daily
// tables and deletes them. It works on whole UTC days that ended before the
// cutoff, oldest first, one day of one monitor at a time. The hourly and
// daily rows are written first. Then the checks of that day go in batches
// of batch rows. It returns the number of deleted rows.
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

// rollupDay writes the daily row for one monitor and day from its hourly
// rows. First each hour of the day without a row gets one from the raw
// checks, as a day before the fill window has none. Existing rows are kept:
// they were written by the job or by an earlier run that did not finish
// deleting, and they are complete while the checks may not be.
func (s *Store) rollupDay(ctx context.Context, monitorID int64, day time.Time) error {
	end := day.Add(24 * time.Hour)
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO hourly (monitor_id, hour, total, ok, avg_latency)
		SELECT ?1, at / 3600 * 3600, COUNT(*), SUM(ok), `+checksAvgLatency+`
		FROM checks WHERE monitor_id = ?1 AND at >= ?2 AND at < ?3
		GROUP BY 2
		ON CONFLICT(monitor_id, hour) DO NOTHING`,
		monitorID, day.Unix(), end.Unix()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO daily (monitor_id, day, total, ok, avg_latency)
		SELECT monitor_id, ?2, SUM(total), SUM(ok), `+hourlyAvgLatency+`
		FROM hourly WHERE monitor_id = ?1 AND hour >= ?3 AND hour < ?4
		GROUP BY monitor_id
		ON CONFLICT(monitor_id, day) DO NOTHING`,
		monitorID, day.Format("2006-01-02"), day.Unix(), end.Unix())
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

// Rollup writes the hourly rows of recent hours and then the daily rows of
// recent days from them.
func (s *Store) Rollup(ctx context.Context, now time.Time) error {
	if err := s.rollupHours(ctx, now); err != nil {
		return err
	}
	return s.rollupDays(ctx, now)
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
		INSERT INTO hourly (monitor_id, hour, total, ok, avg_latency)
		SELECT monitor_id, ?1, COUNT(*), SUM(ok), `+checksAvgLatency+`
		FROM checks
		WHERE monitor_id IN (SELECT id FROM monitors WHERE ?3 OR NOT EXISTS (
				SELECT 1 FROM hourly WHERE hourly.monitor_id = monitors.id AND hourly.hour = ?1))
			AND at >= ?1 AND at < ?2
		GROUP BY monitor_id
		ON CONFLICT(monitor_id, hour) DO UPDATE SET total = excluded.total, ok = excluded.ok, avg_latency = excluded.avg_latency`,
		hour.Unix(), hour.Add(time.Hour).Unix(), replace)
	return err
}

// rollupDays writes the daily rows of the complete UTC days in the 30 days
// before now from the hourly rows, so the dashboard reads daily and not the
// raw checks. It rewrites yesterday, whose last hour can be rewritten after
// an earlier run, and fills each older day that has no row yet, as after a
// downtime. Today gets no row: the dashboard reads today from the hours.
func (s *Store) rollupDays(ctx context.Context, now time.Time) error {
	today := now.UTC().Truncate(24 * time.Hour)
	yesterday := today.AddDate(0, 0, -1)
	if err := s.rollupAll(ctx, yesterday, true); err != nil {
		return err
	}
	for day := today.AddDate(0, 0, -29); day.Before(yesterday); day = day.AddDate(0, 0, 1) {
		if err := s.rollupAll(ctx, day, false); err != nil {
			return err
		}
	}
	return nil
}

// rollupAll writes the daily rows of one UTC day for every monitor with
// hourly rows on it, in one statement. With replace it rewrites the existing
// rows. Without replace it writes only the missing rows, so a filled day
// costs almost nothing.
func (s *Store) rollupAll(ctx context.Context, day time.Time, replace bool) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO daily (monitor_id, day, total, ok, avg_latency)
		SELECT monitor_id, ?1, SUM(total), SUM(ok), `+hourlyAvgLatency+`
		FROM hourly
		WHERE monitor_id IN (SELECT id FROM monitors WHERE ?2 OR id NOT IN (SELECT monitor_id FROM daily WHERE day = ?1))
			AND hour >= ?3 AND hour < ?4
		GROUP BY monitor_id
		ON CONFLICT(monitor_id, day) DO UPDATE SET total = excluded.total, ok = excluded.ok, avg_latency = excluded.avg_latency`,
		day.Format("2006-01-02"), replace, day.Unix(), day.Add(24*time.Hour).Unix())
	return err
}

// nextRun returns the wait until the next run of the hourly job: 30 seconds
// after the next full UTC hour, so the hour that ended gets its rows soon,
// and after midnight the day too. A check takes at most 10 seconds, so
// the checks of the hour are in by then.
func nextRun(now time.Time) time.Duration {
	next := now.UTC().Truncate(time.Hour).Add(30 * time.Second)
	if !next.After(now) {
		next = next.Add(time.Hour)
	}
	return next.Sub(now)
}

// RunRetention deletes expired sessions, writes the hourly and daily rows of
// recent hours and days and runs Retain, at once and then 30 seconds after
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
