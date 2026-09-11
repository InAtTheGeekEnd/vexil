package store

import (
	"context"
	"database/sql"
	"time"
)

// DayStat is the uptime of one monitor on one UTC day.
type DayStat struct {
	Day       string // YYYY-MM-DD
	Total     int
	OK        int
	Incidents int
}

// Percent returns the uptime of the day as 0 to 100.
func (d DayStat) Percent() float64 {
	if d.Total == 0 {
		return 0
	}
	return float64(d.OK) * 100 / float64(d.Total)
}

// DailyStats returns one DayStat per UTC day from since to now, oldest
// first. It merges the daily table with the raw checks, so it works before
// and after the retention job has run. Days without checks have Total 0.
func (s *Store) DailyStats(ctx context.Context, monitorID int64, since, now time.Time) ([]DayStat, error) {
	since, now = since.UTC(), now.UTC()
	first := time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, time.UTC)
	byDay := map[string]*DayStat{}
	var out []DayStat
	for d := first; !d.After(now); d = d.AddDate(0, 0, 1) {
		out = append(out, DayStat{Day: d.Format("2006-01-02")})
	}
	for i := range out {
		byDay[out[i].Day] = &out[i]
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT day, total, ok FROM daily WHERE monitor_id = ? AND day >= ?
		UNION ALL
		SELECT strftime('%Y-%m-%d', at, 'unixepoch'), COUNT(*), SUM(ok)
		FROM checks WHERE monitor_id = ? AND at >= ?
		GROUP BY 1`,
		monitorID, first.Format("2006-01-02"), monitorID, first.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var day string
		var total, ok int
		if err := rows.Scan(&day, &total, &ok); err != nil {
			return nil, err
		}
		if d := byDay[day]; d != nil {
			d.Total += total
			d.OK += ok
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	inc, err := s.db.QueryContext(ctx, `
		SELECT strftime('%Y-%m-%d', started_at, 'unixepoch'), COUNT(*)
		FROM incidents WHERE monitor_id = ? AND started_at >= ? GROUP BY 1`,
		monitorID, first.Unix())
	if err != nil {
		return nil, err
	}
	defer inc.Close()
	for inc.Next() {
		var day string
		var n int
		if err := inc.Scan(&day, &n); err != nil {
			return nil, err
		}
		if d := byDay[day]; d != nil {
			d.Incidents = n
		}
	}
	return out, inc.Err()
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

// Summary counts the checks of a monitor since a time. AvgLatencyMS is the
// average latency of the successful checks, or 0 when there is none.
type Summary struct {
	Total        int
	OK           int
	AvgLatencyMS int64
}

// Percent returns the uptime as 0 to 100 and false when there are no checks.
func (s Summary) Percent() (float64, bool) {
	if s.Total == 0 {
		return 0, false
	}
	return float64(s.OK) * 100 / float64(s.Total), true
}

// Summary reads the check counts and the average latency since a time from
// the raw checks table.
func (s *Store) Summary(ctx context.Context, monitorID int64, since time.Time) (Summary, error) {
	var out Summary
	var avg sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(ok), 0), AVG(CASE WHEN ok = 1 THEN latency_ms END)
		FROM checks WHERE monitor_id = ? AND at >= ?`, monitorID, since.Unix()).Scan(&out.Total, &out.OK, &avg)
	if err != nil {
		return out, err
	}
	out.AvgLatencyMS = int64(avg.Float64 + 0.5)
	return out, nil
}
