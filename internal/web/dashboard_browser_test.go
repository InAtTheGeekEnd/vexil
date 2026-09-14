package web

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// TestStripOrderInChrome takes two monitors down live, the one lower on the
// page first. The newer outage must go to the top of the strip, and a reload
// must show the same order as the live page.
func TestStripOrderInChrome(t *testing.T) {
	path := chromePath()
	if path == "" {
		t.Skip("no Chrome or Chromium found; set VEXIL_CHROME")
	}
	s, st, ids := seedServer(t, seed{name: "Alpha"}, seed{name: "Bravo"})
	ts, _ := browserFixture(t, s, st)
	page, session := openPage(t, path, ts.URL+"/", "light", "")
	ctx := context.Background()
	const strip = "Array.from(document.querySelectorAll('.mon-strip .mon-row')).map(function (r) { return r.dataset.id; }).join(',')"
	alpha, bravo := strconv.FormatInt(ids[0], 10), strconv.FormatInt(ids[1], 10)

	// goDown fails two checks of a monitor and waits for the strip. The
	// first failure also proves that events reach the page.
	goDown := func(id, want string) {
		t.Helper()
		n, _ := strconv.ParseInt(id, 10, 64)
		checked := `document.querySelector('.mon-row[data-id="` + id + `"] .mon-checked')`
		var at string
		page.eval(session, "String("+checked+".dataset.at)", &at)
		if _, err := s.engine.CheckNow(ctx, n); err != nil {
			t.Fatal(err)
		}
		waitFor(t, page, session, "String("+checked+".dataset.at) !== "+strconv.Quote(at), "the check event")
		if _, err := s.engine.CheckNow(ctx, n); err != nil {
			t.Fatal(err)
		}
		waitFor(t, page, session, strip+" === "+strconv.Quote(want), "the strip "+want)
	}
	goDown(bravo, bravo)
	// Outage starts are stored to the second.
	time.Sleep(1100 * time.Millisecond)
	goDown(alpha, alpha+","+bravo)

	var live, reloaded string
	page.eval(session, strip, &live)
	loadPage(t, page, session, ts.URL+"/")
	page.eval(session, strip, &reloaded)
	if want := alpha + "," + bravo; live != want || reloaded != want {
		t.Fatalf("strip live = %s, after a reload = %s, want %s for both", live, reloaded, want)
	}
}

// TestGroupDropTargetInChrome drags a group with the mouse and drops it over
// the rows of another group, not over its heading. The group must land
// there, and the order must be saved.
func TestGroupDropTargetInChrome(t *testing.T) {
	path := chromePath()
	if path == "" {
		t.Skip("no Chrome or Chromium found; set VEXIL_CHROME")
	}
	s, st, ids := seedServer(t, seed{name: "Site"}, seed{name: "Inbox"}, seed{name: "Relay"}, seed{name: "Backup"})
	ctx := context.Background()
	groups := map[string]int64{}
	for _, name := range []string{"Web", "Mail", "Spare"} {
		g := &store.Group{Name: name}
		if err := st.CreateGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
		groups[name] = g.ID
	}
	if err := st.SaveLayout(ctx, nil, []store.Placement{
		{ID: ids[0], GroupID: groups["Web"]},
		{ID: ids[1], GroupID: groups["Mail"]},
		{ID: ids[2], GroupID: groups["Mail"]},
		{ID: ids[3], GroupID: groups["Spare"]},
	}); err != nil {
		t.Fatal(err)
	}
	ts, _ := browserFixture(t, s, st)
	page, session := openPage(t, path, ts.URL+"/", "light", "")
	// A tall viewport keeps every group on the screen for the mouse.
	page.call(session, "Emulation.setDeviceMetricsOverride", map[string]any{"width": 1280, "height": 1400, "deviceScaleFactor": 1, "mobile": false})

	const order = "Array.from(document.querySelectorAll('.dash-group h2')).map(function (h) { return h.textContent; }).join(',')"
	group := func(name string) string {
		return `.dash-group[data-group="` + strconv.FormatInt(groups[name], 10) + `"]`
	}
	middle := func(selector string) (x, y float64) {
		var p struct{ X, Y float64 }
		page.eval(session, "(function () { var r = document.querySelector("+strconv.Quote(selector)+").getBoundingClientRect(); return {X: r.left + r.width / 2, Y: r.top + r.height / 2}; })()", &p)
		return p.X, p.Y
	}
	mouse := func(kind string, x, y float64, buttons int) {
		page.call(session, "Input.dispatchMouseEvent", map[string]any{"type": kind, "x": x, "y": y, "button": "left", "buttons": buttons, "clickCount": 1})
	}
	saved := func(want string) bool {
		for i := 0; i < 100; i++ {
			gs, err := st.Groups(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, g := range gs {
				names = append(names, g.Name)
			}
			if strings.Join(names, ",") == want {
				return true
			}
			time.Sleep(50 * time.Millisecond)
		}
		return false
	}

	for _, tt := range []struct {
		name, drag, over, want string
	}{
		{"up over the rows of the group above", "Spare", group("Mail") + " .mon-row:last-child", "Web,Spare,Mail"},
		{"down over the rows of the group below", "Web", group("Mail") + " .mon-row:first-child", "Spare,Mail,Web"},
	} {
		// Press the grip, move a few pixels toward the target to set the
		// direction, then jump to the middle of the row and drop there.
		x, y := middle(group(tt.drag) + " .group-handle")
		_, to := middle(tt.over)
		step := 6.0
		if to < y {
			step = -6
		}
		mouse("mousePressed", x, y, 1)
		mouse("mouseMoved", x, y+step, 1)
		mouse("mouseMoved", x, to, 1)
		mouse("mouseReleased", x, to, 0)
		waitFor(t, page, session, order+" === "+strconv.Quote(tt.want), tt.name+": the order "+tt.want)
		if !saved(tt.want) {
			t.Fatalf("%s: the saved order is not %s", tt.name, tt.want)
		}
		loadPage(t, page, session, ts.URL+"/")
		waitFor(t, page, session, order+" === "+strconv.Quote(tt.want), tt.name+": the order "+tt.want+" after a reload")
	}
}
