package store

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestRecentBuckets builds the 30-minute buckets of two monitors in Go. The
// checks cross bucket bounds, some fail, some failed ones carry a latency,
// and one bucket has no success. The buckets must match the ones that SQLite
// builds with GROUP BY on the same rows. The plan of the read must have no
// sort, because the rows come in index order.
func TestRecentBuckets(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Now().Add(-6 * time.Hour).Truncate(30 * time.Minute)
	var ids []int64
	for _, name := range []string{"A", "B"} {
		m := &Monitor{Name: name, Type: TypeHTTP, Target: "https://example.com", IntervalS: 60}
		if err := s.CreateMonitor(ctx, m); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	insert := func(c Check) {
		t.Helper()
		if err := s.InsertCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 200; i++ {
		for j, id := range ids {
			ok := (i+j)%7 != 0
			insert(Check{MonitorID: id, At: base.Add(time.Duration(i*97) * time.Second), OK: ok, LatencyMS: int64(20 + (i*13)%90)})
		}
	}
	// A bucket of monitor B with failures only, one of them without a latency.
	failed := base.Add(-3 * time.Hour)
	insert(Check{MonitorID: ids[1], At: failed, Error: "timeout"})
	insert(Check{MonitorID: ids[1], At: failed.Add(time.Minute), LatencyMS: 900, Error: "HTTP 503"})

	since := base.Add(-4 * time.Hour)
	got, err := s.RecentBuckets(ctx, since, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT monitor_id, (at / 1800) * 1800, COUNT(*), SUM(ok), AVG(CASE WHEN ok = 1 THEN latency_ms END)
		FROM checks WHERE at >= ? GROUP BY 1, 2 ORDER BY 1, 2`, since.Unix())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := map[int64][]Bucket{}
	for rows.Next() {
		var id, at int64
		var b Bucket
		var avg sql.NullFloat64
		if err := rows.Scan(&id, &at, &b.Total, &b.OK, &avg); err != nil {
			t.Fatal(err)
		}
		b.At = time.Unix(at, 0)
		b.LatencyMS, b.HasLatency = int64(avg.Float64+0.5), avg.Valid
		want[id] = append(want[id], b)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(got, want) {
		var diff []string
		for _, id := range ids {
			diff = append(diff, fmt.Sprintf("monitor %d:\n got  %+v\n want %+v", id, got[id], want[id]))
		}
		t.Fatalf("buckets differ from GROUP BY:\n%s", strings.Join(diff, "\n"))
	}
	noSuccess := false
	for _, b := range got[ids[1]] {
		noSuccess = noSuccess || (b.Total > 0 && !b.HasLatency)
	}
	if len(got[ids[0]]) < 10 || !noSuccess {
		t.Fatalf("the data does not cover the cases: %d buckets for A, a bucket with no success %v", len(got[ids[0]]), noSuccess)
	}

	if plan := queryPlan(t, s, recentChecksQuery, since.Unix()); strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("the plan sorts:\n%s", plan)
	}
}
