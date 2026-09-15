package web

import (
	"context"
	"html"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

func TestUptime(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	day := now.Truncate(24 * time.Hour) // 12 hours ago
	old := store.Monitor{CreatedAt: now.AddDate(0, 0, -100)}
	inc := func(start, stop time.Duration) store.Incident {
		i := store.Incident{StartedAt: now.Add(start)}
		if stop != 0 {
			i.EndedAt = now.Add(stop)
		}
		return i
	}
	pause := func(start, stop time.Duration) store.Pause {
		p := store.Pause{StartedAt: now.Add(start)}
		if stop != 0 {
			p.EndedAt = now.Add(stop)
		}
		return p
	}
	tests := []struct {
		name      string
		from, to  time.Time
		m         store.Monitor
		incidents []store.Incident
		pauses    []store.Pause
		want      float64
		ok        bool
	}{
		{"no incident is 100", day, now, old, nil, nil, 100, true},
		{"an incident counts its time", day, now, old, []store.Incident{inc(-6*time.Hour, -3*time.Hour)}, nil, 75, true},
		{"an incident is clipped to the window", day, now, old, []store.Incident{inc(-15*time.Hour, -9*time.Hour)}, nil, 75, true},
		{"an open incident lasts to the end", day, now, old, []store.Incident{inc(-3*time.Hour, 0)}, nil, 75, true},
		{"an incident before the window counts nowhere", day, now, old, []store.Incident{inc(-30*time.Hour, -20*time.Hour)}, nil, 100, true},
		{"two incidents add up", day, now, old, []store.Incident{inc(-12*time.Hour, -11*time.Hour), inc(-2*time.Hour, -1*time.Hour)}, nil, float64(10) * 100 / 12, true},
		{"a pause is not live", day, now, old, []store.Incident{inc(-6*time.Hour, -3*time.Hour)}, []store.Pause{pause(-12*time.Hour, -8*time.Hour)}, 62.5, true},
		{"a pause inside an incident leaves both", day, now, old, []store.Incident{inc(-6*time.Hour, -3*time.Hour)}, []store.Pause{pause(-5*time.Hour, -4*time.Hour)}, float64(9) * 100 / 11, true},
		{"an open pause lasts to the end", day, now, old, nil, []store.Pause{pause(-6*time.Hour, 0)}, 100, true},
		{"a window paused throughout has no data", day, now, old, nil, []store.Pause{pause(-13*time.Hour, 0)}, 0, false},
		{"the monitor is live from its creation", day, now, store.Monitor{CreatedAt: now.Add(-4 * time.Hour)}, []store.Incident{inc(-6*time.Hour, -3*time.Hour)}, nil, 75, true},
		{"a window before the creation has no data", day.AddDate(0, 0, -1), day, store.Monitor{CreatedAt: now.Add(-4 * time.Hour)}, nil, nil, 0, false},
		{"an incident of a second in a day", day, day.AddDate(0, 0, 1), old, []store.Incident{inc(-6*time.Hour, -6*time.Hour+time.Second)}, nil, float64(86399) * 100 / 86400, true},
		// 78 days and 1 ns of live time, no incident: exactly 100, not a
		// float64 hair under it.
		{"no incident is exactly 100 for any window", now.Add(-78*24*time.Hour - time.Nanosecond), now, old, nil, nil, 100, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := uptime(tc.from, tc.to, tc.m, tc.incidents, tc.pauses)
			if ok != tc.ok || got < tc.want-1e-9 || got > tc.want+1e-9 {
				t.Errorf("uptime = %v, %v; want %v, %v", got, ok, tc.want, tc.ok)
			}
			if tc.want == 100 && got != 100 {
				t.Errorf("uptime = %v, want exactly 100", got)
			}
		})
	}
	for p, want := range map[float64]string{100: "100", 99.99: "99.99", 99.999: "99.99", 95: "95", 94.999: "94.99", 100 - 1e-12: "100", 87.2: "87.2"} {
		if got := formatPercent(p); got != want {
			t.Errorf("formatPercent(%v) = %q, want %q", p, got, want)
		}
	}

	// The segments: one per UTC day, with the incidents that touched it.
	m := store.Monitor{CreatedAt: day.AddDate(0, 0, -1).Add(12 * time.Hour)}
	incidents := []store.Incident{inc(-13*time.Hour, -11*time.Hour), inc(-2*time.Hour, 0)}
	days := uptimeDays(day.AddDate(0, 0, -2), now, m, incidents, nil)
	if len(days) != 3 {
		t.Fatalf("uptimeDays = %d days, want 3", len(days))
	}
	for i, want := range []uptimeDay{
		{Day: "2026-09-13"},
		{Day: "2026-09-14", Percent: float64(11) * 100 / 12, Incidents: 1, HasData: true},
		{Day: "2026-09-15", Percent: 75, Incidents: 2, HasData: true},
	} {
		got := days[i]
		if got.Day != want.Day || got.HasData != want.HasData || got.Incidents != want.Incidents || got.Percent < want.Percent-1e-9 || got.Percent > want.Percent+1e-9 {
			t.Errorf("day %d = %+v, want %+v", i, got, want)
		}
	}
}

// TestPagesShareUptime gives a public monitor one incident of two hours on
// the day before today. The dashboard, the detail page and the status page
// must show the same percentages and the same one segment below 100.
func TestPagesShareUptime(t *testing.T) {
	s, st := newIdleServer(t, Options{})
	ctx := context.Background()
	now := time.Now()
	m := &store.Monitor{Name: "Shop", Type: store.TypeHTTP, Target: "https://shop.example.com", IntervalS: 60, Public: true, CreatedAt: now.AddDate(0, 0, -100)}
	if err := st.CreateMonitor(ctx, m); err != nil {
		t.Fatal(err)
	}
	yesterday := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	if _, err := st.OpenIncident(ctx, m.ID, yesterday.Add(6*time.Hour), "HTTP 503"); err != nil {
		t.Fatal(err)
	}
	if err := st.CloseIncident(ctx, m.ID, yesterday.Add(8*time.Hour)); err != nil {
		t.Fatal(err)
	}
	ts, c := loggedIn(t, s, st)
	pages := map[string]string{
		"dashboard": body(t, get(t, c, ts.URL+"/")),
		"detail":    body(t, get(t, c, ts.URL+"/monitors/"+strconv.FormatInt(m.ID, 10))),
		"status":    body(t, get(t, c, ts.URL+"/status")),
	}
	pct30 := formatPercent(float64(30*24-2) * 100 / float64(30*24))
	pct90 := formatPercent(float64(90*24-2) * 100 / float64(90*24))
	tests := []struct {
		page, want string
	}{
		{"dashboard", pct30 + "<small>%</small>"},
		{"detail", `<span class="label">Uptime · 30 d</span><span class="value">` + pct30 + `<small>%</small>`},
		{"detail", `<span class="label">Uptime · 90 d</span><span class="value">` + pct90 + `<small>%</small>`},
		{"detail", pct90 + "% over 90 days"},
		{"status", pct90 + "% over 90 days"},
	}
	for _, tc := range tests {
		if !strings.Contains(pages[tc.page], tc.want) {
			t.Errorf("%s page lacks %q", tc.page, tc.want)
		}
	}
	tip := yesterday.Format("2006-01-02") + "\n" + formatPercent(float64(22)*100/24) + "% uptime · 1 incident"
	for name, b := range pages {
		if n := strings.Count(b, `class="seg seg-down"`) + strings.Count(b, `class="seg seg-warn"`); n != 1 {
			t.Errorf("%s page has %d segments below 100, want 1", name, n)
		}
		if !strings.Contains(b, html.EscapeString(tip)) {
			t.Errorf("%s page lacks the tooltip %q", name, tip)
		}
	}
}
