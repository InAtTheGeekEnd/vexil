package web

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// fastRefresh runs before the page scripts of the status page and turns the
// one minute refresh timer into half a second. It shortens every wait of 30
// seconds or more: after the first refresh, status.js asks for a minute less
// the time the fetch took.
const fastRefresh = `(function () { var wait = window.setTimeout; window.setTimeout = function (f, d) { return wait(f, d >= 30000 ? 500 : d); }; })();`

// TestStatusGroupsRefreshInChrome keeps the public status page open while a
// group gets its first public monitor and then loses it. The refresh must
// add the group heading in the dashboard order, with the monitor, and then
// remove both.
func TestStatusGroupsRefreshInChrome(t *testing.T) {
	path := chromePath()
	if path == "" {
		t.Skip("no Chrome or Chromium found; set VEXIL_CHROME")
	}
	s, st, ids := seedServer(t, seed{name: "Site", public: true}, seed{name: "Vault"}, seed{name: "Inbox", public: true})
	setPassword(t, st)
	ctx := context.Background()
	groups := map[string]int64{}
	for _, name := range []string{"Web", "Internal", "Mail"} {
		g := &store.Group{Name: name}
		if err := st.CreateGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
		groups[name] = g.ID
	}
	if err := st.SaveLayout(ctx, nil, []store.Placement{
		{ID: ids[0], GroupID: groups["Web"]},
		{ID: ids[1], GroupID: groups["Internal"]},
		{ID: ids[2], GroupID: groups["Mail"]},
	}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	page, session := openPage(t, path, ts.URL+"/status", "light", fastRefresh)

	// shows is the group headings and the monitor names on the page.
	const shows = "Array.from(document.querySelectorAll('.status-group-name')).map(function (h) { return h.textContent; }).join(',') + '|' + " +
		"Array.from(document.querySelectorAll('.status-name')).map(function (n) { return n.textContent; }).join(',')"
	waitFor(t, page, session, shows+" === 'Web,Mail|Site,Inbox'", "the page without Internal")

	// setPublic sets the status page switch of Vault, the only monitor in
	// Internal.
	setPublic := func(public bool) {
		m, err := st.Monitor(ctx, ids[1])
		if err != nil {
			t.Fatal(err)
		}
		m.Public = public
		if err := st.UpdateMonitor(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	setPublic(true)
	waitFor(t, page, session, shows+" === 'Web,Internal,Mail|Site,Vault,Inbox'", "the refresh to add Internal")
	setPublic(false)
	waitFor(t, page, session, shows+" === 'Web,Mail|Site,Inbox'", "the refresh to remove Internal")
}
