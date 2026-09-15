package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestRecentHours reads the hours of two monitors from 23 hours before the
// current hour. A finished hour with an hourly row comes from the row, even
// when its checks differ. The hours after the newest row come from the
// checks: the hour that ended before the job wrote it and the current hour.
// Rows and checks before the window are left out. Without any hourly row,
// every hour comes from the checks. The plan must search the primary key of
// hourly and the index of checks, not scan them.
func TestRecentHours(t *testing.T) {
	current := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	since := current.Add(-23 * time.Hour)
	// The store returns local times, as time.Unix does.
	at := func(d time.Duration) time.Time { return time.Unix(current.Add(d).Unix(), 0) }
	type hourlyRow struct {
		monitor int // index into the two monitors
		hour    time.Time
		ok      int
		avg     any // nil when no check succeeded
	}
	tests := []struct {
		name   string
		rows   []hourlyRow
		checks []Check // MonitorID is an index into the two monitors
		want   [2][]Bucket
	}{
		{
			name: "hourly rows, then the hours after the newest row from checks",
			rows: []hourlyRow{
				{0, since.Add(-time.Hour), 60, 80},
				{0, current.Add(-5 * time.Hour), 59, 120},
				{1, current.Add(-2 * time.Hour), 0, nil},
			},
			checks: []Check{
				{MonitorID: 0, At: since.Add(-time.Minute), OK: true, LatencyMS: 80},
				{MonitorID: 0, At: current.Add(-5 * time.Hour), OK: true, LatencyMS: 999},
				{MonitorID: 0, At: current.Add(-time.Hour), OK: true, LatencyMS: 100},
				{MonitorID: 0, At: current.Add(-time.Second), OK: true, LatencyMS: 201},
				{MonitorID: 1, At: current.Add(-30 * time.Minute), Error: "timeout"},
				{MonitorID: 0, At: current.Add(time.Minute), OK: true, LatencyMS: 50},
				{MonitorID: 1, At: current.Add(2 * time.Minute), LatencyMS: 900, Error: "HTTP 503"},
			},
			want: [2][]Bucket{
				{
					{At: at(-5 * time.Hour), OK: 59, LatencyMS: 120, HasLatency: true},
					{At: at(-time.Hour), OK: 2, LatencyMS: 151, HasLatency: true},
					{At: at(0), OK: 1, LatencyMS: 50, HasLatency: true},
				},
				{
					{At: at(-2 * time.Hour)},
					{At: at(-time.Hour)},
					{At: at(0)},
				},
			},
		},
		{
			name: "no hourly row yet",
			checks: []Check{
				{MonitorID: 0, At: current.Add(-3 * time.Hour), OK: true, LatencyMS: 100},
				{MonitorID: 0, At: current.Add(time.Minute), OK: true, LatencyMS: 300},
			},
			want: [2][]Bucket{
				{
					{At: at(-3 * time.Hour), OK: 1, LatencyMS: 100, HasLatency: true},
					{At: at(0), OK: 1, LatencyMS: 300, HasLatency: true},
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := openTest(t)
			ctx := context.Background()
			var ids [2]int64
			for i, name := range []string{"A", "B"} {
				m := &Monitor{Name: name, Type: TypeHTTP, Target: "https://example.com", IntervalS: 60}
				if err := s.CreateMonitor(ctx, m); err != nil {
					t.Fatal(err)
				}
				ids[i] = m.ID
			}
			for _, r := range tc.rows {
				if _, err := s.db.ExecContext(ctx, `INSERT INTO hourly (monitor_id, hour, ok, avg_latency) VALUES (?, ?, ?, ?)`,
					ids[r.monitor], r.hour.Unix(), r.ok, r.avg); err != nil {
					t.Fatal(err)
				}
			}
			for _, c := range tc.checks {
				c.MonitorID = ids[c.MonitorID]
				if err := s.InsertCheck(ctx, c); err != nil {
					t.Fatal(err)
				}
			}

			got, err := s.RecentHours(ctx, since)
			if err != nil {
				t.Fatal(err)
			}
			for i, id := range ids {
				if !reflect.DeepEqual(got[id], tc.want[i]) {
					t.Errorf("monitor %d:\n got  %+v\n want %+v", i, got[id], tc.want[i])
				}
			}

			plan := queryPlan(t, s, recentHoursQuery, since.Unix())
			if strings.Contains(plan, "SCAN checks") || strings.Contains(plan, "SCAN hourly") {
				t.Fatalf("the plan scans a table:\n%s", plan)
			}
		})
	}
}
