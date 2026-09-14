package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/engine"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

func TestDashboardGroups(t *testing.T) {
	// Build the store first so the engine restores the states from the
	// database.
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()

	groups := map[string]int64{}
	for _, name := range []string{"Web", "Mail", "Spare"} {
		g := &store.Group{Name: name}
		if err := st.CreateGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
		groups[name] = g.ID
	}
	ids := map[string]int64{}
	now := time.Now()
	for _, name := range []string{"Site", "Shop", "Inbox", "Loose"} {
		m := &store.Monitor{Name: name, Type: store.TypeTCP, Target: "127.0.0.1:1", IntervalS: 900}
		if err := st.CreateMonitor(ctx, m); err != nil {
			t.Fatal(err)
		}
		ids[name] = m.ID
		// Two results each: Shop is down, the others are up.
		for _, ago := range []time.Duration{time.Minute, 2 * time.Minute} {
			c := store.Check{MonitorID: m.ID, At: now.Add(-ago), OK: name != "Shop", LatencyMS: 12}
			if name == "Shop" {
				c.Error = "connection refused"
			}
			if err := st.InsertCheck(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := st.SaveLayout(ctx, nil, []store.Placement{
		{ID: ids["Site"], GroupID: groups["Web"]},
		{ID: ids["Shop"], GroupID: groups["Web"]},
		{ID: ids["Inbox"], GroupID: groups["Mail"]},
		{ID: ids["Loose"]},
	}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng := engine.New(st, engine.Options{Log: log})
	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Stop)
	s, err := New(st, Options{Log: log, Engine: eng})
	if err != nil {
		t.Fatal(err)
	}
	ts, c := loggedIn(t, s, st)

	id := func(n int64) string { return strconv.FormatInt(n, 10) }
	heading := func(name string) string { return `id="group-` + id(groups[name]) + `"` }
	row := func(name string) string { return `data-id="` + id(ids[name]) + `"` }
	// inOrder fails the test unless each marker is on the page after the
	// marker before it.
	inOrder := func(t *testing.T, b string, markers ...string) {
		t.Helper()
		last := -1
		for _, m := range markers {
			i := strings.Index(b, m)
			if i <= last {
				t.Fatalf("%s is at %d, want after %d", m, i, last)
			}
			last = i
		}
	}

	// Shop lifts into the strip above the groups. The strip does not show
	// its group, and the row keeps its group and position for its return.
	// The empty group shows its heading. Loose is in no group, below them.
	b := body(t, get(t, c, ts.URL+"/"))
	inOrder(t, b, "mon-strip", row("Shop"), heading("Web"), row("Site"), heading("Mail"), row("Inbox"),
		heading("Spare"), `data-group="`+id(groups["Spare"])+`" data-empty`, `data-group="0"`, row("Loose"))
	shop := row("Shop") + ` data-state="down" data-group="` + id(groups["Web"]) + `" data-pos="2"`
	if !strings.Contains(b, shop) {
		t.Errorf("the strip row lacks %s", shop)
	}
	if strip := b[strings.Index(b, "mon-strip"):strings.Index(b, "dash-groups")]; strings.Contains(strip, "Web") {
		t.Error("the strip shows the group name")
	}

	// Order the groups Spare, Mail, Web, and move Inbox into Spare, Site into
	// Mail and Loose into Web. The page has Shop in the strip, but a stale
	// page can still post it: the save skips a DOWN monitor.
	form := url.Values{
		"group": {id(groups["Spare"]), id(groups["Mail"]), id(groups["Web"])},
		"id":    {id(ids["Inbox"]), id(ids["Site"]), id(ids["Loose"]), id(ids["Shop"])},
		"in":    {id(groups["Spare"]), id(groups["Mail"]), id(groups["Web"]), "0"},
	}
	if res := postForm(t, c, ts.URL+"/monitors/reorder", form); res.StatusCode != http.StatusNoContent {
		t.Fatalf("reorder = %d", res.StatusCode)
	}
	for _, tt := range []struct {
		name  string
		group int64
		pos   int
	}{
		{"Inbox", groups["Spare"], 1},
		{"Site", groups["Mail"], 1},
		{"Loose", groups["Web"], 1},
		{"Shop", groups["Web"], 2},
	} {
		m, err := st.Monitor(ctx, ids[tt.name])
		if err != nil || m.GroupID != tt.group || m.Position != tt.pos {
			t.Errorf("%s = group %d position %d (%v), want group %d position %d", tt.name, m.GroupID, m.Position, err, tt.group, tt.pos)
		}
	}
	b = body(t, get(t, c, ts.URL+"/"))
	inOrder(t, b, row("Shop"), heading("Spare"), row("Inbox"), heading("Mail"), row("Site"), heading("Web"), row("Loose"))

	for _, bad := range []url.Values{
		{"id": {id(ids["Site"])}},
		{"id": {id(ids["Site"])}, "in": {"x"}},
		{"group": {"x"}},
	} {
		if res := postForm(t, c, ts.URL+"/monitors/reorder", bad); res.StatusCode != http.StatusBadRequest {
			t.Errorf("reorder %v = %d, want 400", bad, res.StatusCode)
		}
	}
}
