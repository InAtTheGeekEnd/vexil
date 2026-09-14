package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// TestDashboardReadsDaily gives a monitor three days before today with 9 of
// 10 checks up, rolled up into daily rows, and then more raw checks on those
// days that are all up. Today has 10 checks, 5 up. The bar and the percent
// must come from the daily rows and today's checks: 32 of 40, or 80%. The
// raw checks would give 88.57%. An incident on one day shows in its tooltip.
func TestDashboardReadsDaily(t *testing.T) {
	s, st := newIdleServer(t, Options{})
	ts, c := loggedIn(t, s, st)
	ctx := context.Background()
	m := &store.Monitor{Name: "API", Type: store.TypeHTTP, Target: "https://api.example.com", IntervalS: 60}
	if err := st.CreateMonitor(ctx, m); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	today := now.UTC().Truncate(24 * time.Hour)
	insert := func(at time.Time, ok bool) {
		t.Helper()
		if err := st.InsertCheck(ctx, store.Check{MonitorID: m.ID, At: at, OK: ok, LatencyMS: 40}); err != nil {
			t.Fatal(err)
		}
	}
	for d := 1; d <= 3; d++ {
		for i := 0; i < 10; i++ {
			insert(today.AddDate(0, 0, -d).Add(12*time.Hour+time.Duration(i)*time.Minute), i > 0)
		}
	}
	if err := st.RollupDays(ctx, now); err != nil {
		t.Fatal(err)
	}
	for d := 1; d <= 3; d++ {
		for i := 0; i < 10; i++ {
			insert(today.AddDate(0, 0, -d).Add(13*time.Hour+time.Duration(i)*time.Minute), true)
		}
	}
	for i := 0; i < 10; i++ {
		insert(now.Add(-time.Duration(i)*100*time.Millisecond), i%2 == 0)
	}
	started := today.AddDate(0, 0, -2).Add(12 * time.Hour)
	if _, err := st.OpenIncident(ctx, m.ID, started, "HTTP 503"); err != nil {
		t.Fatal(err)
	}
	if err := st.CloseIncident(ctx, m.ID, started.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	b := body(t, get(t, c, ts.URL+"/"))
	if want := formatPercent(80) + "<small>%</small>"; !strings.Contains(b, want) {
		t.Errorf("dashboard lacks the 30-day uptime %q", want)
	}
	if strings.Contains(b, formatPercent(float64(62)*100/70)+"<small>%</small>") {
		t.Error("the dashboard counts the raw checks instead of the daily rows")
	}
	if !strings.Contains(b, `1 incident"`) {
		t.Error("the tooltip of the day with an incident does not count it")
	}
	if n := strings.Count(b, `class="seg seg-down"`) + strings.Count(b, `class="seg seg-warn"`); n != 4 {
		t.Errorf("segments with a downtime = %d, want 4: three days at 90%% and today at 50%%", n)
	}
}
