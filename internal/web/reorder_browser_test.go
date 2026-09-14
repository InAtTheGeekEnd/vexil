//go:build chrome

package web

import (
	"context"
	"strconv"
	"testing"
)

// TestReorderFailureReloadsInChrome ends the session and then moves a row
// with the arrow key. The save is sent to the login page, so the page must
// load again and show the login page, not keep an order that the server did
// not store.
func TestReorderFailureReloadsInChrome(t *testing.T) {
	path := requireChrome(t)
	s, st, ids := seedServer(t, seed{name: "Alpha"}, seed{name: "Bravo"})
	ts, _ := browserFixture(t, s, st)
	page, session := openPage(t, path, ts.URL+"/", "light", "")

	if err := st.DeleteAllSessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	grip := `.mon-row[data-id="` + strconv.FormatInt(ids[1], 10) + `"] .mon-handle`
	var ok bool
	page.eval(session, "(document.querySelector("+strconv.Quote(grip)+").focus(), true)", &ok)
	for _, kind := range []string{"rawKeyDown", "keyUp"} {
		page.call(session, "Input.dispatchKeyEvent", map[string]any{"type": kind, "key": "ArrowUp", "code": "ArrowUp", "windowsVirtualKeyCode": 38})
	}
	waitForValue(t, page, session, "location.pathname", "/login", "the login page after the refused save")
}
