package store

import (
	"context"
	"database/sql"
	"slices"
	"strings"
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
// first. A day comes from its daily row. A day of the last 30 days without
// a daily row, as today or yesterday before the job ran, comes from
// MonitorHours. Other days have Total 0.
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

	rolled := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT day, total, ok FROM daily WHERE monitor_id = ? AND day >= ?`,
		monitorID, first.Format("2006-01-02"))
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
		rolled[day] = true
		if d := byDay[day]; d != nil {
			d.Total, d.OK = total, ok
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Only the last 30 days have hours, so the read starts at the first day
	// among them without a daily row.
	today := now.Truncate(24 * time.Hour)
	from := today.AddDate(0, 0, -29)
	if from.Before(first) {
		from = first
	}
	start := today
	for d := from; d.Before(today); d = d.AddDate(0, 0, 1) {
		if !rolled[d.Format("2006-01-02")] {
			start = d
			break
		}
	}
	hours, err := s.MonitorHours(ctx, monitorID, start)
	if err != nil {
		return nil, err
	}
	for _, h := range hours {
		day := h.At.UTC().Format("2006-01-02")
		if d := byDay[day]; d != nil && !rolled[day] {
			d.Total += h.Total
			d.OK += h.OK
		}
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

// DayTotals are the check counts of one monitor on one UTC day.
type DayTotals struct {
	Total int
	OK    int
}

// DailyTotals returns the daily rows of every monitor for the UTC days from
// first to last, by monitor id and day (YYYY-MM-DD), in one query.
func (s *Store) DailyTotals(ctx context.Context, first, last time.Time) (map[int64]map[string]DayTotals, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT monitor_id, day, total, ok FROM daily WHERE day >= ? AND day <= ?`,
		first.UTC().Format("2006-01-02"), last.UTC().Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]map[string]DayTotals{}
	for rows.Next() {
		var id int64
		var day string
		var t DayTotals
		if err := rows.Scan(&id, &day, &t.Total, &t.OK); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[string]DayTotals{}
		}
		out[id][day] = t
	}
	return out, rows.Err()
}

// DailyFromChecks counts the checks of every monitor on the given UTC days,
// by monitor id and day, in one query. It is for days whose daily rows are
// not written yet, as yesterday is in the first seconds after midnight.
func (s *Store) DailyFromChecks(ctx context.Context, days []time.Time) (map[int64]map[string]DayTotals, error) {
	out := map[int64]map[string]DayTotals{}
	if len(days) == 0 {
		return out, nil
	}
	sorted := slices.Clone(days)
	slices.SortFunc(sorted, func(a, b time.Time) int { return a.Compare(b) })
	// One part of the UNION per run of days in a row, so each part searches
	// the index over one time range.
	var parts []string
	var args []any
	for i := 0; i < len(sorted); {
		start := sorted[i].UTC().Truncate(24 * time.Hour)
		end := start.Add(24 * time.Hour)
		j := i + 1
		for j < len(sorted) && !sorted[j].UTC().Truncate(24*time.Hour).After(end) {
			if t := sorted[j].UTC().Truncate(24 * time.Hour); t.Equal(end) {
				end = end.Add(24 * time.Hour)
			}
			j++
		}
		parts = append(parts, `SELECT monitor_id, strftime('%Y-%m-%d', at, 'unixepoch'), COUNT(*), SUM(ok)
			FROM checks WHERE monitor_id IN (SELECT id FROM monitors) AND at >= ? AND at < ? GROUP BY 1, 2`)
		args = append(args, start.Unix(), end.Unix())
		i = j
	}
	rows, err := s.db.QueryContext(ctx, strings.Join(parts, " UNION ALL "), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var day string
		var t DayTotals
		if err := rows.Scan(&id, &day, &t.Total, &t.OK); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[string]DayTotals{}
		}
		out[id][day] = t
	}
	return out, rows.Err()
}

// IncidentDays returns the number of incidents that started on each UTC day
// since a time, by monitor id and day, in one query.
func (s *Store) IncidentDays(ctx context.Context, since time.Time) (map[int64]map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT monitor_id, strftime('%Y-%m-%d', started_at, 'unixepoch'), COUNT(*)
		FROM incidents WHERE started_at >= ? GROUP BY 1, 2`, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]map[string]int{}
	for rows.Next() {
		var id int64
		var day string
		var n int
		if err := rows.Scan(&id, &day, &n); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[string]int{}
		}
		out[id][day] = n
	}
	return out, rows.Err()
}

// Bucket holds the checks of one monitor in one UTC hour.
type Bucket struct {
	At         time.Time // start of the hour
	Total      int
	OK         int
	LatencyMS  int64 // average latency of the successful checks
	HasLatency bool  // false when no check in the hour succeeded
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
	SELECT monitor_id, hour, total, ok, avg_latency FROM hourly
	WHERE monitor_id IN (SELECT id FROM monitors) AND hour >= ?1
	UNION ALL
	SELECT monitor_id, at / 3600 * 3600, COUNT(*), SUM(ok), AVG(CASE WHEN ok = 1 THEN latency_ms END)
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
		if err := rows.Scan(&id, &at, &b.Total, &b.OK, &avg); err != nil {
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
	SELECT hour, total, ok, avg_latency FROM hourly WHERE monitor_id = ?1 AND hour >= ?2
	UNION ALL
	SELECT at / 3600 * 3600, COUNT(*), SUM(ok), AVG(CASE WHEN ok = 1 THEN latency_ms END) FROM checks
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
		if err := rows.Scan(&at, &b.Total, &b.OK, &avg); err != nil {
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
		sum += float64(h.LatencyMS) * float64(h.OK)
		n += h.OK
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
		out.Total += h.Total
		out.OK += h.OK
		if h.HasLatency {
			sum += float64(h.LatencyMS) * float64(h.OK)
			n += h.OK
		}
	}
	if n > 0 {
		out.AvgLatencyMS = int64(sum/float64(n) + 0.5)
	}
	return out, nil
}
