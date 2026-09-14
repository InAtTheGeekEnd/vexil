//go:build chrome

package web

import (
	"context"
	"strconv"
	"testing"

	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// TestReplaceSecretInChrome opens the edit form of a channel with a saved
// secret and clicks Replace. The dots and the button must disappear and the
// empty input must take their place with the focus.
func TestReplaceSecretInChrome(t *testing.T) {
	path := requireChrome(t)
	s, st, _ := seedServer(t)
	ch := &store.Channel{Type: store.ChannelTelegram, Name: "Bot", Enabled: true, Config: map[string]string{"token": "123:SECRET", "chat_id": "42"}}
	if err := st.CreateChannel(context.Background(), ch); err != nil {
		t.Fatal(err)
	}
	ts, _ := browserFixture(t, s, st)
	page, session := startPage(t, path, "")
	loadPage(t, page, session, ts.URL+"/notifications/"+strconv.FormatInt(ch.ID, 10)+"/edit")

	const (
		secret = "document.querySelector('.secret')"
		input  = "document.querySelector('input[name=telegram_token]')"
	)
	// The parentheses matter: "!" binds tighter than "!==".
	shown := func(el string) string { return "(getComputedStyle(" + el + ").display !== 'none')" }
	waitFor(t, page, session, shown(secret)+" && !"+shown(input), "getComputedStyle("+secret+").display", "the saved secret as dots")

	var ok bool
	page.eval(session, "(document.querySelector('[data-replace]').click(), true)", &ok)
	waitFor(t, page, session, "!"+shown(secret)+" && "+shown(input)+" && document.activeElement === "+input,
		"getComputedStyle("+secret+").display + ' / ' + getComputedStyle("+input+").display", "the dots to go and the input to show with the focus")
}
