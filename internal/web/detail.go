package web

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/engine"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// maxIncidents is the length of the incident list on the detail page.
const maxIncidents = 50

// monitorContent is the template content of monitor.html.
type monitorContent struct {
	M          store.Monitor
	TypeLabel  string
	Target     string
	Interval   string
	State      string
	StateLabel string
	Since      string // "Up for 14 days", "" when there is nothing to say
	LastLine   string // "182 ms · checked 12 s ago"
	LastHead   string // "182 ms" or "HTTP 503", the part before the time
	LastAt     int64  // unix seconds of the newest result, 0 when there is none
	Kind       string // "check" or "push"
	PushURL    string
	BadgeURL   string // absolute badge URL for public monitors
	Tiles      []statTile
	Charts     []chartRange // empty for push monitors
	Uptime     []uptimeDay  // 90 days
	UptimePct  string
	Incidents  []incidentRow
}

// statTile is one of the tiles under the title.
type statTile struct {
	Label string
	Value string // "" means no data
	Unit  string
	Class string // "", "warn" or "down"
}

// chartRange is one response chart with its toggle button.
type chartRange struct {
	Key   string // "24h"
	Label string // "24 h"
	From  string // left axis label: "24 h ago"
	SVG   template.HTML
}

// incidentRow is one line of the incident list.
type incidentRow struct {
	Start    string
	Duration string
	Reason   string
	Open     bool
}

// chartRanges are the toggle choices: key, label, window and bucket size.
var chartRanges = []struct {
	Key, Label string
	Window     time.Duration
	Bucket     time.Duration
	TimeFormat string
}{
	{"24h", "24 h", 24 * time.Hour, 15 * time.Minute, "15:04"},
	{"7d", "7 d", 7 * 24 * time.Hour, time.Hour, "Mon 15:04"},
	{"30d", "30 d", 30 * 24 * time.Hour, 6 * time.Hour, "2 Jan 15:04"},
}

func (s *Server) handleMonitor(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadMonitor(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	now := time.Now()
	st := s.monitorStatus(m)

	incidents, err := s.store.Incidents(ctx, m.ID, maxIncidents)
	if err != nil {
		s.serverError(w, err)
		return
	}
	// The uptime comes from the incidents and pauses of the last 90 days.
	since := now.Add(-90 * 24 * time.Hour)
	window, err := s.store.IncidentsSince(ctx, m.ID, since)
	if err != nil {
		s.serverError(w, err)
		return
	}
	pauses, err := s.store.MonitorPauses(ctx, m.ID, since)
	if err != nil {
		s.serverError(w, err)
		return
	}
	// The response tile reads 23 finished hours and the current hour, as
	// the dashboard sparkline does.
	day, err := s.store.Summary(ctx, m.ID, now.UTC().Truncate(time.Hour).Add(-23*time.Hour))
	if err != nil {
		s.serverError(w, err)
		return
	}

	c := monitorContent{
		M:          m,
		TypeLabel:  typeLabels[m.Type],
		Target:     m.Target,
		Interval:   intervalLabel(m.IntervalS),
		State:      stateClass(st.State),
		StateLabel: stateLabel(st.State),
		Since:      sinceLine(m, st, incidents, now),
		Kind:       kind(m),
		Tiles:      tiles(m, st, day, window, pauses, now),
		Incidents:  incidentRows(incidents, now),
	}
	c.LastHead, c.LastAt = lastParts(m, st)
	c.LastLine = lastLine(m, st, now)
	today := now.UTC().Truncate(24 * time.Hour)
	c.Uptime = uptimeDays(today.AddDate(0, 0, -89), now, m, window, pauses)
	c.UptimePct = percentText(uptime(since, now, m, window, pauses))
	if m.Public {
		c.BadgeURL = s.absoluteURL(r, fmt.Sprintf("/badge/%d.svg", m.ID))
	}
	if m.Type == store.TypePush {
		c.PushURL = s.pushURL(r, m.PushToken)
	} else {
		if c.Charts, err = s.charts(r, m.ID, now); err != nil {
			s.serverError(w, err)
			return
		}
	}
	s.render(w, http.StatusOK, "monitor.html", pageData{Content: c, Live: true, Down: s.downCount()})
}

// charts renders the response chart for every range. A range without at
// least two points gets an empty SVG.
func (s *Server) charts(r *http.Request, id int64, now time.Time) ([]chartRange, error) {
	out := make([]chartRange, 0, len(chartRanges))
	for _, cr := range chartRanges {
		// The 7-day and 30-day charts, with buckets of an hour or more, read
		// the hourly rows. The 24-hour chart reads the checks.
		read := s.store.LatencySeries
		if cr.Bucket >= time.Hour {
			read = s.store.HourlyLatencySeries
		}
		series, err := read(r.Context(), id, now.Add(-cr.Window), cr.Bucket)
		if err != nil {
			return nil, err
		}
		c := chartRange{Key: cr.Key, Label: cr.Label, From: cr.Label + " ago"}
		if len(series) >= 2 {
			points := make([]chartPoint, len(series))
			for i, p := range series {
				points[i] = chartPoint{
					Value: float64(p.LatencyMS),
					Label: fmt.Sprintf("%s · %d ms", p.At.Local().Format(cr.TimeFormat), p.LatencyMS),
				}
			}
			c.SVG = responseChart("chart-"+cr.Key, points)
		}
		out = append(out, c)
	}
	return out, nil
}

// tiles builds the stat tiles: uptime for 24 h, 7 d, 30 d and 90 d from the
// incidents and pauses, the average response time, and the certificate
// expiry for HTTPS monitors.
func tiles(m store.Monitor, st engine.Status, day store.Summary, incidents []store.Incident, pauses []store.Pause, now time.Time) []statTile {
	var out []statTile
	for _, w := range []struct {
		label string
		back  time.Duration
	}{
		{"Uptime · 24 h", 24 * time.Hour},
		{"Uptime · 7 d", 7 * 24 * time.Hour},
		{"Uptime · 30 d", 30 * 24 * time.Hour},
		{"Uptime · 90 d", 90 * 24 * time.Hour},
	} {
		t := statTile{Label: w.label}
		if p, ok := uptime(now.Add(-w.back), now, m, incidents, pauses); ok {
			t.Value, t.Unit = formatPercent(p), "%"
		}
		out = append(out, t)
	}
	if m.Type != store.TypePush {
		t := statTile{Label: "Avg response"}
		if day.OK > 0 {
			t.Value, t.Unit = fmt.Sprint(day.AvgLatencyMS), "ms"
		}
		out = append(out, t)
	}
	if m.Type == store.TypeHTTP && strings.HasPrefix(strings.ToLower(m.Target), "https://") {
		t := statTile{Label: "Certificate"}
		if !st.CertExpiry.IsZero() {
			left := st.CertExpiry.Sub(now)
			switch {
			case left <= 0:
				t.Value, t.Class = "Expired", "down"
			case left < 14*24*time.Hour:
				t.Value, t.Unit, t.Class = fmt.Sprint(int(left.Hours()/24)), "days", "warn"
			default:
				t.Value, t.Unit = fmt.Sprint(int(left.Hours()/24)), "days"
			}
		}
		out = append(out, t)
	}
	return out
}

// sinceLine is the "Up for 14 days" or "Down for 6 minutes" line. It is
// empty for paused and pending monitors, whose state label says enough.
// incidents is the list from the store, newest first.
func sinceLine(m store.Monitor, st engine.Status, incidents []store.Incident, now time.Time) string {
	switch st.State {
	case engine.Down:
		since := st.LastAt
		if len(incidents) > 0 && incidents[0].Open() {
			since = incidents[0].StartedAt
		}
		return "Down for " + longDuration(now.Sub(since))
	case engine.Up:
		since := m.CreatedAt
		if len(incidents) > 0 && !incidents[0].Open() {
			since = incidents[0].EndedAt
		}
		return "Up for " + longDuration(now.Sub(since))
	}
	return ""
}

// lastParts returns the head of the result line ("182 ms" or "HTTP 503")
// and the time of the newest result. live.js updates both.
func lastParts(m store.Monitor, st engine.Status) (string, int64) {
	if st.State == engine.Paused || st.LastAt.IsZero() {
		return "", 0
	}
	if m.Type == store.TypePush {
		return "", st.LastAt.Unix()
	}
	if st.Last.OK {
		return fmt.Sprintf("%d ms", st.Last.Latency.Milliseconds()), st.LastAt.Unix()
	}
	return st.Last.Error, st.LastAt.Unix()
}

// lastLine describes the newest result: "182 ms · checked 12 s ago",
// "HTTP 503 · checked 12 s ago" or "Pushed just now".
func lastLine(m store.Monitor, st engine.Status, now time.Time) string {
	head, at := lastParts(m, st)
	if at == 0 || head == "" {
		return checkedLine(m, st, now)
	}
	return head + " · " + strings.ToLower(checkedLine(m, st, now))
}

func incidentRows(incidents []store.Incident, now time.Time) []incidentRow {
	out := make([]incidentRow, 0, len(incidents))
	for _, inc := range incidents {
		row := incidentRow{Start: inc.StartedAt.Local().Format("2 Jan 2006, 15:04"), Reason: inc.Reason, Open: inc.Open()}
		if inc.Open() {
			row.Duration = "Ongoing · " + shortDuration(now.Sub(inc.StartedAt))
		} else {
			row.Duration = shortDuration(inc.EndedAt.Sub(inc.StartedAt))
		}
		if row.Reason == "" {
			row.Reason = "Unknown"
		}
		out = append(out, row)
	}
	return out
}

// longDuration reads well after "for": "less than a minute", "6 minutes",
// "3 hours", "14 days".
func longDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour")
	}
	return plural(int(d.Hours()/24), "day")
}

// shortDuration fits in a table cell: "42 s", "6 m", "2 h 10 m", "3 d 4 h".
func shortDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	days := int(d.Hours() / 24)
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0 && hours > 0:
		return fmt.Sprintf("%d d %d h", days, hours)
	case days > 0:
		return fmt.Sprintf("%d d", days)
	case hours > 0 && mins > 0:
		return fmt.Sprintf("%d h %d m", hours, mins)
	case hours > 0:
		return fmt.Sprintf("%d h", hours)
	case mins > 0:
		return fmt.Sprintf("%d m", mins)
	}
	return fmt.Sprintf("%d s", int(d.Seconds()))
}
