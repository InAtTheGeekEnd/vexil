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
// first. A day comes from the daily table when the retention job has
// written it, and from the raw checks otherwise. Days without checks have
// Total 0.
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
	raw, err := s.db.QueryContext(ctx, `
		SELECT strftime('%Y-%m-%d', at, 'unixepoch'), COUNT(*), SUM(ok)
		FROM checks WHERE monitor_id = ? AND at >= ? GROUP BY 1`, monitorID, first.Unix())
	if err != nil {
		return nil, err
	}
	defer raw.Close()
	for raw.Next() {
		var day string
		var total, ok int
		if err := raw.Scan(&day, &total, &ok); err != nil {
			return nil, err
		}
		if d := byDay[day]; d != nil && !rolled[day] {
			d.Total, d.OK = total, ok
		}
	}
	if err := raw.Err(); err != nil {
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

// Bucket holds the checks of one monitor in one time bucket.
type Bucket struct {
	At         time.Time // start of the bucket
	Total      int
	OK         int
	LatencyMS  int64 // average latency of the successful checks
	HasLatency bool  // false when no check in the bucket succeeded
}

// recentChecksQuery reads the checks of every monitor since a time. The IN
// over the monitors lets SQLite search the covering index on (monitor_id,
// at, ok, latency_ms), and the ORDER BY is the index order, so SQLite does
// not sort.
const recentChecksQuery = `
	SELECT monitor_id, at, ok, latency_ms FROM checks
	WHERE monitor_id IN (SELECT id FROM monitors) AND at >= ?
	ORDER BY monitor_id, at`

// RecentBuckets returns the checks of every monitor since a time, grouped
// into buckets of the given size counted from the Unix epoch, oldest first,
// by monitor id, in one query. A bucket of 30 minutes starts at a UTC
// midnight, so the buckets from today's midnight on add up to today. The
// rows come in order, so the buckets are built in one pass as they stream in.
func (s *Store) RecentBuckets(ctx context.Context, since time.Time, bucket time.Duration) (map[int64][]Bucket, error) {
	secs := int64(bucket / time.Second)
	if secs <= 0 {
		secs = 60
	}
	rows, err := s.db.QueryContext(ctx, recentChecksQuery, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64][]Bucket{}
	var (
		id, at, ok     int64
		latency        sql.NullInt64
		open           bool
		curID, start   int64
		cur            Bucket
		latSum, latNum int64
	)
	flush := func() {
		if !open {
			return
		}
		if latNum > 0 {
			cur.LatencyMS, cur.HasLatency = int64(float64(latSum)/float64(latNum)+0.5), true
		}
		out[curID] = append(out[curID], cur)
	}
	for rows.Next() {
		if err := rows.Scan(&id, &at, &ok, &latency); err != nil {
			return nil, err
		}
		if b := at / secs * secs; !open || id != curID || b != start {
			flush()
			open, curID, start = true, id, b
			cur, latSum, latNum = Bucket{At: time.Unix(b, 0)}, 0, 0
		}
		cur.Total++
		if ok == 1 {
			cur.OK++
			if latency.Valid {
				latSum += latency.Int64
				latNum++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	flush()
	return out, nil
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
