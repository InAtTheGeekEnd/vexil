package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/engine"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// inOrder fails the test unless each marker is in b after the marker before
// it.
func inOrder(t *testing.T, b string, markers ...string) {
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

// TestStripOrder renders DOWN monitors whose outages started at different
// times. The strip shows the newest outage first, and equal starts in
// monitor id order, whatever the order of the monitors on the page.
func TestStripOrder(t *testing.T) {
	// Build the store first so the engine restores DOWN from the database.
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	now := time.Now()
	// Alpha is first on the page but has the oldest outage. Bravo and
	// Charlie went down in the same second. Delta is up.
	started := map[string]time.Time{"Alpha": now.Add(-3 * time.Hour), "Bravo": now.Add(-time.Hour), "Charlie": now.Add(-time.Hour)}
	ids := map[string]string{}
	for _, name := range []string{"Alpha", "Bravo", "Charlie", "Delta"} {
		m := &store.Monitor{Name: name, Type: store.TypeTCP, Target: "127.0.0.1:1", IntervalS: 900}
		if err := st.CreateMonitor(ctx, m); err != nil {
			t.Fatal(err)
		}
		ids[name] = strconv.FormatInt(m.ID, 10)
		at, down := started[name]
		for _, age := range []time.Duration{time.Minute, 2 * time.Minute} {
			check := store.Check{MonitorID: m.ID, At: now.Add(-age), OK: !down, LatencyMS: 12}
			if down {
				check.Error = "connection refused"
			}
			if err := st.InsertCheck(ctx, check); err != nil {
				t.Fatal(err)
			}
		}
		if down {
			if _, err := st.OpenIncident(ctx, m.ID, at, "connection refused"); err != nil {
				t.Fatal(err)
			}
		}
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

	b := body(t, get(t, c, ts.URL+"/"))
	row := func(name string) string { return `data-id="` + ids[name] + `"` }
	inOrder(t, b, "mon-strip", row("Bravo"), row("Charlie"), row("Alpha"), "mon-ungrouped", row("Delta"))
	want := row("Alpha") + ` data-state="down" data-group="0" data-pos="1" data-since="` + strconv.FormatInt(started["Alpha"].Unix(), 10) + `"`
	if !strings.Contains(b, want) {
		t.Errorf("the strip row lacks %s", want)
	}
}

// TestStateEventSince checks that a DOWN state event carries the start of
// the open incident, so the page can place the row in the strip.
func TestStateEventSince(t *testing.T) {
	s, st := newTestServer(t, Options{})
	ts, c := loggedIn(t, s, st)
	ctx := context.Background()
	m := &store.Monitor{Name: "Site", Type: store.TypeTCP, Target: "127.0.0.1:1", IntervalS: 900}
	if err := st.CreateMonitor(ctx, m); err != nil {
		t.Fatal(err)
	}
	started := time.Unix(1_800_000_000, 0)
	if _, err := st.OpenIncident(ctx, m.ID, started, "connection refused"); err != nil {
		t.Fatal(err)
	}

	res := get(t, c, ts.URL+"/events")
	defer res.Body.Close()
	r := bufio.NewReader(res.Body)
	if line, _ := readEvent(t, r); line != "retry: 3000" {
		t.Fatalf("first line = %q, want the retry hint", line)
	}
	s.engine.Hub().Publish(engine.Event{MonitorID: m.ID, Prev: engine.Up, State: engine.Down, At: time.Now()})
	name, data := readEvent(t, r)
	var se stateEvent
	if err := json.Unmarshal([]byte(data), &se); name != "state" || err != nil || se.Since != started.Unix() {
		t.Fatalf("event = %q %s, want a state event with since %d", name, data, started.Unix())
	}
}
