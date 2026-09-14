package web

import (
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/InAtTheGeekEnd/vexil/internal/engine"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// maxNameLen limits a monitor, channel or group name. The forms set the same
// limit with maxlength.
const maxNameLen = 60

const nameTooLong = "The name is too long. Use 60 characters or fewer."

// Interval choices from SPEC.md section 5.
var intervals = []struct {
	Seconds int
	Label   string
}{
	{30, "30 seconds"},
	{60, "1 minute"},
	{300, "5 minutes"},
	{900, "15 minutes"},
	{1800, "30 minutes"},
	{3600, "1 hour"},
	{21600, "6 hours"},
	{43200, "12 hours"},
	{86400, "24 hours"},
}

// typeLabels are the display names of the monitor types.
var typeLabels = map[string]string{
	store.TypeHTTP: "HTTP(S)",
	store.TypeTCP:  "TCP port",
	store.TypePing: "Ping",
	store.TypeDNS:  "DNS",
	store.TypePush: "Push",
}

// --- Dashboard ---

type dashboardContent struct {
	Headline  string
	State     string // state class of the headline dot
	Count     string // "3 monitors"
	Monitors  int
	Down      []monitorRow // the DOWN strip above the groups
	Groups    []dashGroup
	Ungrouped []monitorRow // the monitors in no group, below the groups
}

// dashGroup is a group heading with the rows of its monitors that are not
// down.
type dashGroup struct {
	ID   int64
	Name string
	Rows []monitorRow
}

type monitorRow struct {
	ID         int64
	Name       string
	Target     string
	State      string // up, down, paused, pending
	StateLabel string
	Uptime     []uptimeDay
	UptimePct  string // "" when there is no data
	Spark      template.HTML
	Checked    string
	CheckedAt  int64  // unix seconds of the newest result, 0 when there is none
	Kind       string // "check" or "push", the verb for the checked line
	Position   int
	Group      int64 // the id of the group the row belongs in, 0 for none
	DownSince  int64 // unix seconds of the start of the open incident, 0 for none
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	monitors, err := s.store.Monitors(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
	groups, err := s.store.Groups(ctx)
	if err != nil {
		s.serverError(w, err)
		return
	}
	now := time.Now()
	content := dashboardContent{Monitors: len(monitors), Groups: make([]dashGroup, len(groups))}
	index := make(map[int64]int, len(groups)) // group id to index in content.Groups
	for i, g := range groups {
		content.Groups[i] = dashGroup{ID: g.ID, Name: g.Name}
		index[g.ID] = i
	}
	var down, pending, active int
	for _, m := range monitors {
		st := s.monitorStatus(m)
		row := monitorRow{
			ID:         m.ID,
			Name:       m.Name,
			Target:     targetLine(m),
			State:      stateClass(st.State),
			StateLabel: stateLabel(st.State),
			Checked:    checkedLine(m, st, now),
			Kind:       kind(m),
			Position:   m.Position,
		}
		if st.State != engine.Paused && !st.LastAt.IsZero() {
			row.CheckedAt = st.LastAt.Unix()
		}
		switch st.State {
		case engine.Down:
			down++
		case engine.Pending:
			pending++
		}
		if !m.Paused {
			active++
		}
		days, err := s.store.DailyStats(ctx, m.ID, now.AddDate(0, 0, -29), now)
		if err != nil {
			s.serverError(w, err)
			return
		}
		row.Uptime, row.UptimePct = uptimeBar(days)
		series, err := s.store.LatencySeries(ctx, m.ID, now.Add(-24*time.Hour), 30*time.Minute)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if len(series) >= 2 {
			values := make([]float64, len(series))
			for i, p := range series {
				values[i] = float64(p.LatencyMS)
			}
			row.Spark = sparkline(fmt.Sprintf("spark-%d", m.ID), values)
		}
		// The store returns the drag order, so appending keeps it.
		gi, grouped := index[m.GroupID]
		if grouped {
			row.Group = m.GroupID
		}
		switch {
		case st.State == engine.Down:
			if row.DownSince, err = s.downSince(ctx, m.ID); err != nil {
				s.serverError(w, err)
				return
			}
			content.Down = append(content.Down, row)
		case grouped:
			content.Groups[gi].Rows = append(content.Groups[gi].Rows, row)
		default:
			content.Ungrouped = append(content.Ungrouped, row)
		}
	}
	sortStrip(content.Down)
	content.Headline, content.State = headline(down, pending, active)
	content.Count = plural(len(monitors), "monitor")
	s.render(w, http.StatusOK, "dashboard.html", pageData{Content: content, Live: true, Down: down, Nav: "dashboard"})
}

func headline(down, pending, active int) (string, string) {
	switch {
	case down > 0:
		if down == 1 {
			return "1 monitor is down", "down"
		}
		return fmt.Sprintf("%d monitors are down", down), "down"
	case active == 0:
		return "All monitors are paused", "paused"
	case pending == active:
		return "Waiting for the first checks", "pending"
	}
	return "All systems operational", "up"
}

// uptimeBar turns 30 day stats into bar segments and a 30-day percentage.
func uptimeBar(days []store.DayStat) ([]uptimeDay, string) {
	out := make([]uptimeDay, len(days))
	var total, ok int
	for i, d := range days {
		out[i] = uptimeDay{Day: d.Day, Percent: d.Percent(), Incidents: d.Incidents, HasData: d.Total > 0}
		total += d.Total
		ok += d.OK
	}
	if total == 0 {
		return out, ""
	}
	return out, formatPercent(float64(ok) * 100 / float64(total))
}

// monitorStatus returns the live status, or PAUSED / PENDING when the engine
// has none.
func (s *Server) monitorStatus(m store.Monitor) engine.Status {
	if s.engine != nil {
		if st, ok := s.engine.Status(m.ID); ok {
			return st
		}
	}
	if m.Paused {
		return engine.Status{State: engine.Paused}
	}
	return engine.Status{State: engine.Pending}
}

func stateClass(st engine.State) string {
	switch st {
	case engine.Up:
		return "up"
	case engine.Down:
		return "down"
	case engine.Paused:
		return "paused"
	}
	return "pending"
}

func stateLabel(st engine.State) string {
	return capitalize(stateClass(st))
}

func targetLine(m store.Monitor) string {
	if m.Type == store.TypePush {
		return "Heartbeat every " + intervalLabel(m.IntervalS)
	}
	return m.Target
}

func intervalLabel(secs int) string {
	for _, iv := range intervals {
		if iv.Seconds == secs {
			return iv.Label
		}
	}
	return fmt.Sprintf("%d seconds", secs)
}

// kind is the verb of a monitor's result line: "check" or "push".
func kind(m store.Monitor) string {
	if m.Type == store.TypePush {
		return "push"
	}
	return "check"
}

// checkedLine is the muted text at the end of a row: "Checked 12 s ago".
// live.js builds the same text in the browser.
func checkedLine(m store.Monitor, st engine.Status, now time.Time) string {
	switch {
	case st.State == engine.Paused:
		return "Paused"
	case st.LastAt.IsZero():
		return "Waiting for the first " + kind(m)
	}
	return capitalize(kind(m)) + "ed " + ago(now.Sub(st.LastAt))
}

// ago formats a duration as "12 s ago", "3 m ago", "2 h ago" or "3 d ago".
func ago(d time.Duration) string {
	switch {
	case d < 5*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%d s ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d m ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d d ago", int(d.Hours()/24))
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// --- Detail helpers ---

func (s *Server) pushURL(r *http.Request, token string) string {
	return s.absoluteURL(r, "/push/"+token)
}

// absoluteURL prefixes a path with the base URL, or with the scheme and
// host of the request when no base URL is set.
func (s *Server) absoluteURL(r *http.Request, path string) string {
	base := s.baseURL
	if base == "" {
		scheme := "http"
		if isHTTPS(r) {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	return base + path
}

// loadMonitor reads the {id} path value. It renders 404 and returns false
// when the monitor does not exist.
func (s *Server) loadMonitor(w http.ResponseWriter, r *http.Request) (store.Monitor, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderError(w, http.StatusNotFound)
		return store.Monitor{}, false
	}
	m, err := s.store.Monitor(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.renderError(w, http.StatusNotFound)
		return store.Monitor{}, false
	}
	if err != nil {
		s.serverError(w, err)
		return store.Monitor{}, false
	}
	return m, true
}

// --- Actions: pause, resume, delete, reorder ---

func (s *Server) handleMonitorPause(w http.ResponseWriter, r *http.Request) {
	s.setPaused(w, r, true)
}

func (s *Server) handleMonitorResume(w http.ResponseWriter, r *http.Request) {
	s.setPaused(w, r, false)
}

func (s *Server) setPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	m, ok := s.loadMonitor(w, r)
	if !ok {
		return
	}
	if err := s.store.SetPaused(r.Context(), m.ID, paused); err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.reload(r, m.ID); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/monitors/%d", m.ID), http.StatusSeeOther)
}

func (s *Server) handleMonitorDelete(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadMonitor(w, r)
	if !ok {
		return
	}
	del := s.store.DeleteMonitor
	if s.engine != nil {
		// The engine drops a check result that arrives during the delete.
		del = s.engine.DeleteMonitor
	}
	if err := del(r.Context(), m.ID); err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.reload(r, m.ID); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleReorder saves the dashboard layout. The body is a form with one
// "group" value per group in display order, and one "id" value per monitor
// in display order with a matching "in" value: the id of its group, or 0
// for none. A DOWN monitor waits in the strip, so the save skips it: it
// keeps its group and position.
func (s *Server) handleReorder(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	groups, okGroups := parseIDs(r.Form["group"])
	ids, okIDs := parseIDs(r.Form["id"])
	in, okIn := parseIDs(r.Form["in"])
	if !okGroups || !okIDs || !okIn || len(ids) != len(in) {
		http.Error(w, "bad layout", http.StatusBadRequest)
		return
	}
	var monitors []store.Placement
	for i, id := range ids {
		if s.isDown(id) {
			continue
		}
		monitors = append(monitors, store.Placement{ID: id, GroupID: in[i]})
	}
	if err := s.store.SaveLayout(r.Context(), groups, monitors); err != nil {
		s.serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseIDs parses form values as ids. It returns false when a value is not
// a number.
func parseIDs(values []string) ([]int64, bool) {
	out := make([]int64, 0, len(values))
	for _, v := range values {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, false
		}
		out = append(out, id)
	}
	return out, true
}

// isDown reports whether the engine has the monitor as DOWN.
func (s *Server) isDown(id int64) bool {
	if s.engine == nil {
		return false
	}
	st, ok := s.engine.Status(id)
	return ok && st.State == engine.Down
}

// reload tells the engine about a store change. It is a no-op without an
// engine.
func (s *Server) reload(r *http.Request, id int64) error {
	if s.engine == nil {
		return nil
	}
	return s.engine.Reload(r.Context(), id)
}

// firstCheck runs the first check after a save so the detail page shows a
// result at once. Errors are logged, never shown: the monitor is saved.
func (s *Server) firstCheck(r *http.Request, id int64) {
	if s.engine == nil {
		return
	}
	if _, err := s.engine.CheckNow(r.Context(), id); err != nil && !errors.Is(err, engine.ErrNoChecker) {
		s.log.Error("first check", "monitor", id, "err", err)
	}
}

// --- Add and edit form ---

// monitorForm holds the form fields and their errors. It is the template
// content for monitor_form.html.
type monitorForm struct {
	ID         int64
	Type       string
	Name       string
	URL        string
	Host       string
	Port       string
	Hostname   string
	Keyword    string
	ExpectedIP string
	Interval   int
	Public     bool
	Errors     map[string]string
	Intervals  []struct {
		Seconds int
		Label   string
	}
}

func newMonitorForm() monitorForm {
	return monitorForm{Type: store.TypeHTTP, Interval: 60, Errors: map[string]string{}, Intervals: intervals}
}

// formFromMonitor fills the form from a saved monitor.
func formFromMonitor(m store.Monitor) monitorForm {
	f := newMonitorForm()
	f.ID, f.Type, f.Name = m.ID, m.Type, m.Name
	f.Interval, f.Public = m.IntervalS, m.Public
	f.Keyword, f.ExpectedIP = m.Keyword, m.ExpectedIP
	switch m.Type {
	case store.TypeHTTP:
		f.URL = m.Target
	case store.TypeTCP:
		host, port, err := net.SplitHostPort(m.Target)
		if err != nil {
			host = m.Target
		}
		f.Host, f.Port = host, port
	case store.TypePing:
		f.Host = m.Target
	case store.TypeDNS:
		f.Hostname = m.Target
	}
	return f
}

// parseMonitorForm reads the posted fields. It does not validate.
func parseMonitorForm(r *http.Request) monitorForm {
	f := newMonitorForm()
	get := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	f.Type = get("type")
	f.Name = get("name")
	f.URL = get("url")
	f.Host = get("host")
	f.Port = get("port")
	f.Hostname = get("hostname")
	f.Keyword = get("keyword")
	f.ExpectedIP = get("expected_ip")
	f.Interval, _ = strconv.Atoi(get("interval"))
	f.Public = r.FormValue("public") != ""
	return f
}

// hostWithExtras is the form error for a host with a scheme, a port or a
// path around it.
const hostWithExtras = "Enter a host name or IP address, without http:// or a port."

// bareHost reports whether s is only a host name or an IP address. A
// scheme, a port, a path or brackets around it can never pass a check.
// With ip false, only a name counts: an IPv6 address has colons.
func bareHost(s string, ip bool) bool {
	if ip && net.ParseIP(s) != nil {
		return true
	}
	return !strings.ContainsAny(s, ":/[]@ ")
}

// validate fills f.Errors and returns the monitor to save. It fills an
// empty name from the target.
func (f *monitorForm) validate() store.Monitor {
	m := store.Monitor{ID: f.ID, Type: f.Type, IntervalS: f.Interval, Public: f.Public}
	if _, ok := typeLabels[f.Type]; !ok {
		f.Errors["type"] = "Choose a type."
		return m
	}
	switch f.Type {
	case store.TypeHTTP:
		u, err := url.Parse(f.URL)
		if f.URL == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			f.Errors["url"] = "Enter a full URL that starts with http:// or https://."
		} else {
			m.Target = f.URL
			m.Keyword = f.Keyword
			f.fillName(u.Hostname())
		}
	case store.TypeTCP:
		port, err := strconv.Atoi(f.Port)
		if f.Host == "" {
			f.Errors["host"] = "Enter a host name or IP address."
		} else if !bareHost(f.Host, true) {
			f.Errors["host"] = hostWithExtras
		}
		if f.Port == "" || err != nil || port < 1 || port > 65535 {
			f.Errors["port"] = "Enter a number between 1 and 65535."
		}
		if f.Errors["host"] == "" && f.Errors["port"] == "" {
			m.Target = net.JoinHostPort(f.Host, f.Port)
			f.fillName(f.Host)
		}
	case store.TypePing:
		if f.Host == "" {
			f.Errors["host"] = "Enter a host name or IP address."
		} else if !bareHost(f.Host, true) {
			f.Errors["host"] = hostWithExtras
		} else {
			m.Target = f.Host
			f.fillName(f.Host)
		}
	case store.TypeDNS:
		if f.Hostname == "" {
			f.Errors["hostname"] = "Enter a host name."
		} else if !bareHost(f.Hostname, false) {
			f.Errors["hostname"] = "Enter a host name, without http:// or a port."
		} else {
			m.Target = f.Hostname
			f.fillName(f.Hostname)
		}
		if f.ExpectedIP != "" && net.ParseIP(f.ExpectedIP) == nil {
			f.Errors["expected_ip"] = "Enter a valid IP address, or leave the field empty."
		} else {
			m.ExpectedIP = f.ExpectedIP
		}
	case store.TypePush:
		m.Target = "push"
	}
	if f.Name == "" {
		f.Errors["name"] = "Enter a name."
	} else if utf8.RuneCountInString(f.Name) > maxNameLen {
		f.Errors["name"] = nameTooLong
	}
	m.Name = f.Name
	if !validInterval(f.Interval) {
		f.Errors["interval"] = "Choose an interval."
	}
	return m
}

func validInterval(secs int) bool {
	for _, iv := range intervals {
		if iv.Seconds == secs {
			return true
		}
	}
	return false
}

func (f *monitorForm) fillName(host string) {
	if f.Name == "" {
		f.Name = host
	}
}

func (s *Server) handleMonitorNewForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "monitor_form.html", pageData{Content: newMonitorForm()})
}

func (s *Server) handleMonitorCreate(w http.ResponseWriter, r *http.Request) {
	f := parseMonitorForm(r)
	m := f.validate()
	if len(f.Errors) > 0 {
		s.render(w, http.StatusBadRequest, "monitor_form.html", pageData{Content: f})
		return
	}
	if err := s.store.CreateMonitor(r.Context(), &m); err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.reload(r, m.ID); err != nil {
		s.serverError(w, err)
		return
	}
	s.firstCheck(r, m.ID)
	http.Redirect(w, r, fmt.Sprintf("/monitors/%d", m.ID), http.StatusSeeOther)
}

func (s *Server) handleMonitorEditForm(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadMonitor(w, r)
	if !ok {
		return
	}
	s.render(w, http.StatusOK, "monitor_form.html", pageData{Content: formFromMonitor(m)})
}

func (s *Server) handleMonitorUpdate(w http.ResponseWriter, r *http.Request) {
	old, ok := s.loadMonitor(w, r)
	if !ok {
		return
	}
	f := parseMonitorForm(r)
	f.ID = old.ID
	f.Type = old.Type // the type is fixed after creation
	m := f.validate()
	if len(f.Errors) > 0 {
		s.render(w, http.StatusBadRequest, "monitor_form.html", pageData{Content: f})
		return
	}
	m.Paused = old.Paused
	if err := s.store.UpdateMonitor(r.Context(), m); err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.reload(r, m.ID); err != nil {
		s.serverError(w, err)
		return
	}
	s.firstCheck(r, m.ID)
	http.Redirect(w, r, fmt.Sprintf("/monitors/%d", m.ID), http.StatusSeeOther)
}
