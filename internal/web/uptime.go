package web

import (
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// uptime returns the uptime of a monitor from one time to another as 0 to
// 100: the live time outside incidents divided by the live time. The
// monitor is live from its creation, outside its pauses. ok is false when
// the monitor was not live in the window. An open incident or pause lasts
// to the end of the window. No incident in the window gives 100.
//
// Every page computes its uptime here: the bar segments, the dashboard
// percentage and the tiles of the detail page. Failed checks that opened no
// incident count nowhere.
func uptime(from, to time.Time, m store.Monitor, incidents []store.Incident, pauses []store.Pause) (float64, bool) {
	if m.CreatedAt.After(from) {
		from = m.CreatedAt
	}
	live := overlap(from, to, from, to)
	for _, p := range pauses {
		live -= overlap(from, to, p.StartedAt, end(p.EndedAt, to))
	}
	if live <= 0 {
		return 0, false
	}
	var down time.Duration
	for _, inc := range incidents {
		start, stop := clip(from, to, inc.StartedAt, end(inc.EndedAt, to))
		d := overlap(from, to, start, stop)
		for _, p := range pauses {
			d -= overlap(start, stop, p.StartedAt, end(p.EndedAt, to))
		}
		down += d
	}
	return float64(live-down) * 100 / float64(live), true
}

// uptimeDays builds the segments of an uptime bar: one per UTC day from
// first to today, each with its uptime and the incidents that touched it.
func uptimeDays(first, now time.Time, m store.Monitor, incidents []store.Incident, pauses []store.Pause) []uptimeDay {
	today := now.UTC().Truncate(24 * time.Hour)
	var out []uptimeDay
	for d := first; !d.After(today); d = d.AddDate(0, 0, 1) {
		to := d.AddDate(0, 0, 1)
		if to.After(now) {
			to = now
		}
		day := uptimeDay{Day: d.Format("2006-01-02")}
		day.Percent, day.HasData = uptime(d, to, m, incidents, pauses)
		for _, inc := range incidents {
			if inc.StartedAt.Before(to) && (inc.Open() || !inc.EndedAt.Before(d)) {
				day.Incidents++
			}
		}
		out = append(out, day)
	}
	return out
}

// percentText formats an uptime for a page, or "" when there is none.
func percentText(p float64, ok bool) string {
	if !ok {
		return ""
	}
	return formatPercent(p)
}

// end returns the end of a period, or to when the period is open.
func end(ended, to time.Time) time.Time {
	if ended.IsZero() {
		return to
	}
	return ended
}

// clip cuts a period to a window.
func clip(from, to, start, stop time.Time) (time.Time, time.Time) {
	if start.Before(from) {
		start = from
	}
	if stop.After(to) {
		stop = to
	}
	return start, stop
}

// overlap returns how long a period lies inside a window, at least 0.
func overlap(from, to, start, stop time.Time) time.Duration {
	start, stop = clip(from, to, start, stop)
	if d := stop.Sub(start); d > 0 {
		return d
	}
	return 0
}
