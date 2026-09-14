package store

import (
	"context"
	"strings"
	"testing"
)

// queryPlan returns the lines of EXPLAIN QUERY PLAN for a query.
func queryPlan(t *testing.T, s *Store, query string, args ...any) string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(plan, "\n")
}

// TestChecksIndexCovers asks SQLite for the plan of a stats read over a time
// range. The index on checks must cover ok and latency_ms, so the read never
// goes to the table rows: with 100 monitors, the dashboard reads 144,000
// checks of the last 24 hours on every request.
func TestChecksIndexCovers(t *testing.T) {
	s := openTest(t)
	plan := queryPlan(t, s, `SELECT at, ok, latency_ms FROM checks WHERE monitor_id = ? AND at >= ?`, 1, 0)
	if !strings.Contains(plan, "USING COVERING INDEX checks_monitor_at") {
		t.Fatalf("plan:\n%s\nwant a search with the covering index checks_monitor_at", plan)
	}
}
