package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

func TestChannelLifecycle(t *testing.T) {
	s, st := newTestServer(t, Options{})
	ts, c := loggedIn(t, s, st)

	var hits []string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		hits = append(hits, r.Header.Get("Authorization")+" "+string(b))
	}))
	defer target.Close()

	// Empty state.
	if b := body(t, get(t, c, ts.URL+"/notifications")); !strings.Contains(b, "No channels yet") || !strings.Contains(b, `aria-current="page"`) {
		t.Fatal("empty notifications page lacks the empty state or the active nav item")
	}

	// The form shows the type cards and the ntfy fields.
	b := body(t, get(t, c, ts.URL+"/notifications/new"))
	for _, want := range []string{"type-cards", `name="ntfy_url"`, `name="ntfy_token"`, `name="email_host"`, "Bot token"} {
		if !strings.Contains(b, want) {
			t.Errorf("channel form lacks %q", want)
		}
	}

	// Validation.
	res := postForm(t, c, ts.URL+"/notifications/new", url.Values{"type": {"ntfy"}, "ntfy_url": {"ntfy.sh/x"}})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body(t, res), "Enter a full URL") {
		t.Fatalf("bad form = %d", res.StatusCode)
	}
	res = postForm(t, c, ts.URL+"/notifications/new", url.Values{"type": {"ntfy"}, "name": {strings.Repeat("a", 61)}, "ntfy_url": {target.URL + "/alerts"}})
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body(t, res), "60 characters or fewer") {
		t.Fatalf("long name form = %d", res.StatusCode)
	}

	// Create. The browser posts the inputs of every type; only the chosen
	// type's inputs count. The name comes from the type.
	res = postForm(t, c, ts.URL+"/notifications/new", url.Values{
		"type": {"ntfy"}, "slack_url": {""}, "discord_url": {""}, "webhook_url": {""}, "telegram_token": {""},
		"ntfy_url": {target.URL + "/alerts"}, "ntfy_token": {"tk_secret"},
	})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/notifications" {
		t.Fatalf("create = %d -> %q", res.StatusCode, res.Header.Get("Location"))
	}
	channels, _ := st.Channels(context.Background())
	if len(channels) != 1 || channels[0].Name != "ntfy" || channels[0].Config["token"] != "tk_secret" || !channels[0].Enabled {
		t.Fatalf("saved channels = %+v", channels)
	}
	id := strconv.FormatInt(channels[0].ID, 10)

	// The list shows the channel without its secret.
	b = body(t, get(t, c, ts.URL+"/notifications"))
	if !strings.Contains(b, ">ntfy<") || !strings.Contains(b, `aria-checked="true"`) || strings.Contains(b, "tk_secret") {
		t.Fatal("list page is wrong or leaks the token")
	}

	// The edit form hides the secret. An empty secret keeps the old value.
	b = body(t, get(t, c, ts.URL+"/notifications/"+id+"/edit"))
	if strings.Contains(b, "tk_secret") || !strings.Contains(b, "data-replace") || strings.Contains(b, "type-cards") {
		t.Fatal("edit form leaks the token, lacks Replace or shows the type cards")
	}
	res = postForm(t, c, ts.URL+"/notifications/"+id+"/edit", url.Values{"ntfy_url": {target.URL + "/alerts2"}, "ntfy_token": {""}, "name": {"Phone"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("edit = %d", res.StatusCode)
	}
	ch, _ := st.Channel(context.Background(), channels[0].ID)
	if ch.Name != "Phone" || ch.Config["url"] != target.URL+"/alerts2" || ch.Config["token"] != "tk_secret" {
		t.Fatalf("edited channel = %+v", ch)
	}

	// Send test reaches the fake ntfy server with the token.
	res = postForm(t, c, ts.URL+"/notifications/"+id+"/test", nil)
	if b := body(t, res); res.StatusCode != http.StatusOK || !strings.Contains(b, "Test sent to Phone") {
		t.Fatalf("test = %d", res.StatusCode)
	}
	if len(hits) != 1 || !strings.HasPrefix(hits[0], "Bearer tk_secret ") || !strings.Contains(hits[0], `"topic":"alerts2"`) {
		t.Fatalf("hits = %q", hits)
	}
	target.Close()
	res = postForm(t, c, ts.URL+"/notifications/"+id+"/test", nil)
	if b := body(t, res); !strings.Contains(b, "The test to Phone failed") || strings.Contains(b, "tk_secret") {
		t.Fatal("failed test lacks the error line or leaks the token")
	}

	// Toggle off and on.
	postForm(t, c, ts.URL+"/notifications/"+id+"/toggle", nil)
	if ch, _ = st.Channel(context.Background(), channels[0].ID); ch.Enabled {
		t.Fatal("channel still enabled after toggle")
	}
	if b := body(t, get(t, c, ts.URL+"/notifications")); !strings.Contains(b, `aria-checked="false"`) {
		t.Fatal("list does not show the channel as off")
	}
	postForm(t, c, ts.URL+"/notifications/"+id+"/toggle", nil)
	if ch, _ = st.Channel(context.Background(), channels[0].ID); !ch.Enabled {
		t.Fatal("channel not enabled after second toggle")
	}

	// Delete.
	res = postForm(t, c, ts.URL+"/notifications/"+id+"/delete", nil)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete = %d", res.StatusCode)
	}
	if res := get(t, c, ts.URL+"/notifications/"+id+"/edit"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted channel edit = %d, want 404", res.StatusCode)
	}
}

func TestPushoverChannelForm(t *testing.T) {
	s, st := newTestServer(t, Options{})
	ts, c := loggedIn(t, s, st)
	ctx := context.Background()

	// The form shows the Pushover card and fields with the default times.
	b := body(t, get(t, c, ts.URL+"/notifications/new"))
	for _, want := range []string{`value="pushover"`, `name="pushover_user"`, `name="pushover_token"`, `name="pushover_repeat" value="1">`,
		`name="pushover_retry" value="1"`, `name="pushover_expire" value="60"`, `data-needs="repeat"`, "Repeat until acknowledged"} {
		if !strings.Contains(b, want) {
			t.Errorf("channel form lacks %q", want)
		}
	}

	keys := url.Values{"type": {"pushover"}, "pushover_user": {"uUSERKEY123"}, "pushover_token": {"aAPPTOKEN456"}}
	with := func(extra url.Values) url.Values {
		v := url.Values{}
		for k, vs := range keys {
			v[k] = vs
		}
		for k, vs := range extra {
			v[k] = vs
		}
		return v
	}
	tests := []struct {
		name string
		form url.Values
		want string
	}{
		{"missing keys", url.Values{"type": {"pushover"}}, "Enter the user key."},
		{"repeat checks the retry interval", with(url.Values{"pushover_repeat": {"1"}, "pushover_retry": {"0"}, "pushover_expire": {"60"}}), "Enter a number between 1 and 60."},
		{"repeat checks the expiry time", with(url.Values{"pushover_repeat": {"1"}, "pushover_retry": {"1"}, "pushover_expire": {""}}), "Enter the expiry time."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := postForm(t, c, ts.URL+"/notifications/new", tc.form)
			if b := body(t, res); res.StatusCode != http.StatusBadRequest || !strings.Contains(b, tc.want) || strings.Contains(b, "APPTOKEN") {
				t.Fatalf("status = %d, want 400 with %q and no token", res.StatusCode, tc.want)
			}
		})
	}

	// Create with repeat on.
	res := postForm(t, c, ts.URL+"/notifications/new", with(url.Values{"pushover_repeat": {"1"}, "pushover_retry": {"2"}, "pushover_expire": {"90"}}))
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create = %d", res.StatusCode)
	}
	channels, _ := st.Channels(ctx)
	if len(channels) != 1 || channels[0].Name != "Pushover" || channels[0].Config["repeat"] != "1" || channels[0].Config["retry"] != "2" || channels[0].Config["user"] != "uUSERKEY123" {
		t.Fatalf("saved channels = %+v", channels)
	}
	id := strconv.FormatInt(channels[0].ID, 10)

	// The list and the edit form show the switch state, never the keys.
	b = body(t, get(t, c, ts.URL+"/notifications"))
	if !strings.Contains(b, "Pushover · DOWN alerts repeat until acknowledged") || strings.Contains(b, "USERKEY") || strings.Contains(b, "APPTOKEN") {
		t.Fatal("list lacks the Pushover summary or leaks a key")
	}
	b = body(t, get(t, c, ts.URL+"/notifications/"+id+"/edit"))
	if !strings.Contains(b, `name="pushover_repeat" value="1" checked`) || !strings.Contains(b, `name="pushover_retry" value="2"`) || strings.Contains(b, "USERKEY") || strings.Contains(b, "APPTOKEN") {
		t.Fatal("edit form lacks the saved repeat settings or leaks a key")
	}

	// Turn repeat off. Empty secrets keep the keys, and the times are not checked.
	res = postForm(t, c, ts.URL+"/notifications/"+id+"/edit", url.Values{"pushover_user": {""}, "pushover_token": {""}, "pushover_retry": {"0"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("edit = %d", res.StatusCode)
	}
	ch, _ := st.Channel(ctx, channels[0].ID)
	if ch.Config["repeat"] != "" || ch.Config["user"] != "uUSERKEY123" || ch.Config["token"] != "aAPPTOKEN456" {
		t.Fatalf("edited channel = %+v", ch)
	}
	if b := body(t, get(t, c, ts.URL+"/notifications")); !strings.Contains(b, "Pushover · Normal priority") {
		t.Fatal("list does not show normal priority after repeat is off")
	}
}

func TestChannelListShowsLastError(t *testing.T) {
	s, st := newTestServer(t, Options{})
	ctx := context.Background()
	ch := &store.Channel{Type: store.ChannelSlack, Name: "Ops", Config: map[string]string{"url": "https://hooks.slack.com/services/T/B/SECRET"}, Enabled: true}
	if err := st.CreateChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}
	if err := st.SetChannelError(ctx, ch.ID, "HTTP 403", time.Now().Add(-3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	ts, c := loggedIn(t, s, st)
	b := body(t, get(t, c, ts.URL+"/notifications"))
	if !strings.Contains(b, "Failed 3 m ago: HTTP 403") || !strings.Contains(b, "POST to hooks.slack.com") || strings.Contains(b, "SECRET") {
		t.Fatal("list lacks the last error or leaks the webhook URL")
	}
}
