package web

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/engine"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// incidentWindow is how far back the public page lists incidents.
const incidentWindow = 14 * 24 * time.Hour

// statusContent is the data of the public status page.
type statusContent struct {
	Headline  string
	State     string
	Rows      []statusRow
	Incidents []statusIncident
}

type statusRow struct {
	Name       string
	State      string
	StateLabel string
	Uptime     []uptimeDay // 90 days
	UptimePct  string
}

type statusIncident struct {
	Monitor  string
	Start    string
	Duration string
	Reason   string
	Open     bool
}

// handleStatus renders the public page. It shows public monitors only and
// never their targets.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	monitors, err := s.store.Monitors(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
	now := time.Now()
	var c statusContent
	var down, pending, active int
	for _, m := range monitors {
		if !m.Public {
			continue
		}
		st := s.monitorStatus(m)
		row := statusRow{Name: m.Name, State: stateClass(st.State), StateLabel: stateLabel(st.State)}
		switch st.State {
		case engine.Down:
			down++
		case engine.Pending:
			pending++
		}
		if !m.Paused {
			active++
		}
		days, err := s.store.DailyStats(ctx, m.ID, now.AddDate(0, 0, -89), now)
		if err != nil {
			s.serverError(w, err)
			return
		}
		row.Uptime, row.UptimePct = uptimeBar(days)
		c.Rows = append(c.Rows, row)
	}
	c.Headline, c.State = headline(down, pending, active)
	if len(c.Rows) == 0 {
		c.Headline, c.State = "Nothing to show yet", "pending"
	}
	incidents, err := s.store.RecentIncidents(ctx, now.Add(-incidentWindow), true)
	if err != nil {
		s.serverError(w, err)
		return
	}
	for _, inc := range incidents {
		row := incidentRows([]store.Incident{inc.Incident}, now)[0]
		c.Incidents = append(c.Incidents, statusIncident{Monitor: inc.MonitorName, Start: row.Start, Duration: row.Duration, Reason: row.Reason, Open: row.Open})
	}
	w.Header().Set("Cache-Control", "no-cache")
	s.render(w, http.StatusOK, "status.html", pageData{Content: c, Down: down})
}

// --- Badge ---

// badgeColors are the right-hand colors by state class.
var badgeColors = map[string]string{
	"up":      "#16A34A",
	"down":    "#DC2626",
	"paused":  "#9CA3AF",
	"pending": "#9CA3AF",
}

// handleBadge serves /badge/{id}.svg for public monitors. A private or
// missing monitor gives the same 404, so the badge URL tells nothing.
func (s *Server) handleBadge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=60")
	name, ok := strings.CutSuffix(r.PathValue("file"), ".svg")
	if !ok {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(name, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m, err := s.store.Monitor(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !m.Public) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	state := stateClass(s.monitorStatus(m).State)
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write([]byte(badgeSVG(m.Name, state, badgeColors[state])))
}

// badgeSVG draws a flat two-part badge: the label on dark grey, the value
// on the status color. Text widths are fixed with textLength so every
// viewer renders the same shape.
func badgeSVG(label, value, color string) string {
	const charW, pad, h = 6.5, 6.0, 20.0
	lw := float64(len([]rune(label)))*charW + 2*pad
	vw := float64(len([]rune(value)))*charW + 2*pad
	w := lw + vw
	esc := template.HTMLEscapeString
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%s" height="%s" role="img" aria-label="%s: %s">`+
		`<title>%s: %s</title>`+
		`<clipPath id="r"><rect width="%s" height="%s" rx="3" fill="#fff"/></clipPath>`+
		`<g clip-path="url(#r)"><rect width="%s" height="%s" fill="#555"/><rect x="%s" width="%s" height="%s" fill="%s"/></g>`+
		`<g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" font-size="11">`+
		`<text x="%s" y="14" textLength="%s">%s</text>`+
		`<text x="%s" y="14" textLength="%s">%s</text></g></svg>`,
		ftoa(w), ftoa(h), esc(label), esc(value),
		esc(label), esc(value),
		ftoa(w), ftoa(h),
		ftoa(lw), ftoa(h), ftoa(lw), ftoa(vw), ftoa(h), color,
		ftoa(lw/2), ftoa(lw-2*pad), esc(label),
		ftoa(lw+vw/2), ftoa(vw-2*pad), esc(value))
}
