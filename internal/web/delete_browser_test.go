//go:build chrome

package web

import (
	"net/http"
	"strconv"
	"testing"
)

// TestDeleteDownMonitorInChrome opens the dashboard with one monitor up and
// one down, and deletes the DOWN monitor from another client. The open page
// must drop the row, the "(1)" in the title, the red tab icon and the down
// headline, without a reload.
func TestDeleteDownMonitorInChrome(t *testing.T) {
	path := requireChrome(t)
	s, st, ids := seedServer(t, seed{name: "Alpha"}, seed{name: "Bravo", down: true})
	ts, c := browserFixture(t, s, st)
	page, session := openPage(t, path, ts.URL+"/", "light", liveOpen)
	waitLive(t, page, session)
	var ok bool
	page.eval(session, "(window.probeSamePage = true)", &ok)

	bravo := strconv.FormatInt(ids[1], 10)
	if res := postForm(t, c, ts.URL+"/monitors/"+bravo+"/delete", nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete = %d, want 303", res.StatusCode)
	}

	b := s.currentBrand()
	title := "Dashboard · " + b.Name
	row := `document.querySelector('.mon-row[data-id="` + bravo + `"]')`
	waitFor(t, page, session, "!"+row+" && document.title === "+strconv.Quote(title),
		"document.title + (window.probeSamePage ? '' : ', page reloaded')", "the row and the down count to go")
	waitForIcon(t, page, session, b.FaviconUpURL, "the up icon")
	waitForValue(t, page, session, "document.querySelector('.dash-head h1').textContent.trim()", "All systems operational", "the headline")
	if page.eval(session, "window.probeSamePage === true", &ok); !ok {
		t.Fatal("the page reloaded, want an update in place")
	}
}
