package store

import (
	"context"
	"database/sql"
	"time"
)

// Bucket holds the successful checks of one monitor in one UTC hour.
type Bucket struct {
	At         time.Time // start of the hour
	Samples    int       // successful checks: the weight of LatencyMS
	LatencyMS  int64     // average latency of the successful checks
	HasLatency bool      // false when no check in the hour succeeded
}

// recentHoursQuery reads the hours of every monitor from ?1 on: the hourly
// rows, and the hours after the newest row from the checks. Those are the
// current hour and an hour that ended before the job wrote it. The job
// writes an hour for all monitors in one statement and fills older hours
// first, so no hour before the newest row is missing. The IN over the
// monitors lets SQLite search the primary key of hourly and the covering
// index of checks.
const recentHoursQuery = `
	WITH rolled(until) AS (
		SELECT COALESCE(MAX(hour) + 3600, ?1) FROM hourly
		WHERE monitor_id IN (SELECT id FROM monitors) AND hour >= ?1)
	SELECT monitor_id, hour, samples, avg_latency FROM hourly
	WHERE monitor_id IN (SELECT id FROM monitors) AND hour >= ?1
	UNION ALL
	SELECT monitor_id, at / 3600 * 3600, SUM(ok), AVG(CASE WHEN ok = 1 THEN latency_ms END)
	FROM checks
	WHERE monitor_id IN (SELECT id FROM monitors) AND at >= (SELECT until FROM rolled)
	GROUP BY 1, 2
	ORDER BY 1, 2`

// RecentHours returns the UTC hours of every monitor from since on, oldest
// first, by monitor id, in one query. since must be the start of an hour.
// A finished hour comes from its hourly row. The hours after the newest
// hourly row come from the checks.
func (s *Store) RecentHours(ctx context.Context, since time.Time) (map[int64][]Bucket, error) {
	rows, err := s.db.QueryContext(ctx, recentHoursQuery, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]Bucket{}
	for rows.Next() {
		var id, at int64
		var b Bucket
		var avg sql.NullFloat64
		if err := rows.Scan(&id, &at, &b.Samples, &avg); err != nil {
			return nil, err
		}
		b.At = time.Unix(at, 0)
		b.LatencyMS, b.HasLatency = int64(avg.Float64+0.5), avg.Valid
		out[id] = append(out[id], b)
	}
	return out, rows.Err()
}

// monitorHoursQuery is recentHoursQuery for one monitor: the hourly rows of
// monitor ?1 from hour ?2 on, and the hours after its newest row from the
// checks.
const monitorHoursQuery = `
	WITH rolled(until) AS (
		SELECT COALESCE(MAX(hour) + 3600, ?2) FROM hourly WHERE monitor_id = ?1 AND hour >= ?2)
	SELECT hour, samples, avg_latency FROM hourly WHERE monitor_id = ?1 AND hour >= ?2
	UNION ALL
	SELECT at / 3600 * 3600, SUM(ok), AVG(CASE WHEN ok = 1 THEN latency_ms END) FROM checks
	WHERE monitor_id = ?1 AND at >= (SELECT until FROM rolled)
	GROUP BY 1
	ORDER BY 1`

// MonitorHours returns the UTC hours of one monitor from since on, oldest
// first. since is rounded down to the start of an hour. A finished hour
// comes from its hourly row, and the hours after the newest row come from
// the checks.
func (s *Store) MonitorHours(ctx context.Context, monitorID int64, since time.Time) ([]Bucket, error) {
	rows, err := s.db.QueryContext(ctx, monitorHoursQuery, monitorID, since.Truncate(time.Hour).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bucket
	for rows.Next() {
		var at int64
		var b Bucket
		var avg sql.NullFloat64
		if err := rows.Scan(&at, &b.Samples, &avg); err != nil {
			return nil, err
		}
		b.At = time.Unix(at, 0)
		b.LatencyMS, b.HasLatency = int64(avg.Float64+0.5), avg.Valid
		out = append(out, b)
	}
	return out, rows.Err()
}

// LatencyPoint is the average latency of successful checks in one time bucket.
type LatencyPoint struct {
	At        time.Time // start of the bucket
	LatencyMS int64
}

// LatencySeries returns the average latency of successful checks from since
// to now, grouped into buckets of the given size, oldest first. Buckets
// without a successful check are left out.
func (s *Store) LatencySeries(ctx context.Context, monitorID int64, since time.Time, bucket time.Duration) ([]LatencyPoint, error) {
	secs := int64(bucket / time.Second)
	if secs <= 0 {
		secs = 60
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT (at / ?) * ?, AVG(latency_ms) FROM checks
		WHERE monitor_id = ? AND ok = 1 AND at >= ? AND latency_ms IS NOT NULL
		GROUP BY 1 ORDER BY 1`, secs, secs, monitorID, since.Unix())
	if err != nil {
		return nil, err
	}
	return latencyPoints(rows)
}

// HourlyLatencySeries is LatencySeries for buckets of an hour or more, built
// from MonitorHours. Each hour counts by its successful checks.
func (s *Store) HourlyLatencySeries(ctx context.Context, monitorID int64, since time.Time, bucket time.Duration) ([]LatencyPoint, error) {
	secs := max(int64(bucket/time.Second), 3600)
	hours, err := s.MonitorHours(ctx, monitorID, since)
	if err != nil {
		return nil, err
	}
	var out []LatencyPoint
	var start int64
	var sum float64
	var n int
	flush := func() {
		if n > 0 {
			out = append(out, LatencyPoint{At: time.Unix(start, 0), LatencyMS: int64(sum/float64(n) + 0.5)})
		}
	}
	for _, h := range hours {
		if !h.HasLatency {
			continue
		}
		if b := h.At.Unix() / secs * secs; n == 0 || b != start {
			flush()
			start, sum, n = b, 0, 0
		}
		sum += float64(h.LatencyMS) * float64(h.Samples)
		n += h.Samples
	}
	flush()
	return out, nil
}

// latencyPoints reads rows of a bucket start and an average latency.
func latencyPoints(rows *sql.Rows) ([]LatencyPoint, error) {
	defer rows.Close()
	var out []LatencyPoint
	for rows.Next() {
		var at int64
		var avg sql.NullFloat64
		if err := rows.Scan(&at, &avg); err != nil {
			return nil, err
		}
		out = append(out, LatencyPoint{At: time.Unix(at, 0), LatencyMS: int64(avg.Float64 + 0.5)})
	}
	return out, rows.Err()
}

// Summary counts the successful checks of a monitor since a time.
// AvgLatencyMS is their average latency, or 0 when there is none.
type Summary struct {
	Samples      int
	AvgLatencyMS int64
}

// Summary sums the hours of a monitor from since on, from MonitorHours.
// since is rounded down to the start of an hour. The average latency counts
// each hour by its successful checks.
func (s *Store) Summary(ctx context.Context, monitorID int64, since time.Time) (Summary, error) {
	var out Summary
	hours, err := s.MonitorHours(ctx, monitorID, since)
	if err != nil {
		return out, err
	}
	var sum float64
	var n int
	for _, h := range hours {
		out.Samples += h.Samples
		if h.HasLatency {
			sum += float64(h.LatencyMS) * float64(h.Samples)
			n += h.Samples
		}
	}
	if n > 0 {
		out.AvgLatencyMS = int64(sum/float64(n) + 0.5)
	}
	return out, nil
}
