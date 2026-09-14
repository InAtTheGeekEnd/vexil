package web

import (
	"bytes"
	"context"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/notify"
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
			if b := body(t, res); res.StatusCode != http.StatusBadRequest || !strings.Contains(b, tc.want) || strings.Contains(b, "data-replace") {
				t.Fatalf("status = %d, want 400 with %q and no saved secret", res.StatusCode, tc.want)
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

// lockedBuffer collects log output. Server goroutines write to it while
// the test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// formInputValue returns the value of the named input on a page.
func formInputValue(page, name string) (string, bool) {
	m := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`).FindStringSubmatch(page)
	if m == nil {
		return "", false
	}
	return html.UnescapeString(m[1]), true
}

func TestCreateFormKeepsSecretsAfterError(t *testing.T) {
	logs := &lockedBuffer{}
	s, st := newTestServer(t, Options{Log: slog.New(slog.NewTextHandler(logs, nil))})
	ts, c := loggedIn(t, s, st)
	ctx := context.Background()
	long := strings.Repeat("a", 61)

	// Every secret contains "SECRET", so the test can count where it shows.
	tests := []struct {
		typ     string
		form    url.Values        // has a validation error in a field that is not secret
		fix     url.Values        // fields that fix the error
		secrets map[string]string // field key to typed secret
		want    string            // the error on the page
	}{
		{"email", url.Values{"email_host": {"smtp.example.com"}, "email_port": {"0"}, "email_from": {"a@example.com"}, "email_to": {"b@example.com"}},
			url.Values{"email_port": {"587"}}, map[string]string{"password": "pwEMAILSECRET"}, "Enter a number between 1 and 65535."},
		{"slack", url.Values{"name": {long}}, url.Values{"name": {"Ops"}},
			map[string]string{"url": "https://hooks.slack.com/services/T0/B0/SLACKSECRET"}, "60 characters or fewer"},
		{"discord", url.Values{"name": {long}}, url.Values{"name": {"Ops"}},
			map[string]string{"url": "https://discord.com/api/webhooks/1/DISCORDSECRET"}, "60 characters or fewer"},
		{"telegram", url.Values{}, url.Values{"telegram_chat_id": {"42"}},
			map[string]string{"token": "123:TELEGRAMSECRET"}, "Enter the chat id."},
		{"ntfy", url.Values{"ntfy_url": {"ntfy.sh/alerts"}}, url.Values{"ntfy_url": {"https://ntfy.sh/alerts"}},
			map[string]string{"token": "tkNTFYSECRET"}, "Enter a full URL"},
		{"pushover", url.Values{"pushover_repeat": {"1"}, "pushover_retry": {"0"}, "pushover_expire": {"60"}}, url.Values{"pushover_retry": {"1"}},
			map[string]string{"user": "uPUSHOVERUSERSECRET", "token": "aPUSHOVERTOKENSECRET"}, "Enter a number between 1 and 60."},
		{"webhook", url.Values{"name": {long}}, url.Values{"name": {"Hook"}},
			map[string]string{"url": "https://example.com/hooks/uptime?key=WEBHOOKSECRET"}, "60 characters or fewer"},
	}
	covered := map[string]bool{}
	for _, tc := range tests {
		covered[tc.typ] = true
		t.Run(tc.typ, func(t *testing.T) {
			form := url.Values{"type": {tc.typ}}
			for k, v := range tc.form {
				form[k] = v
			}
			for key, secret := range tc.secrets {
				form.Set(tc.typ+"_"+key, secret)
			}
			res := postForm(t, c, ts.URL+"/notifications/new", form)
			page := body(t, res)
			if res.StatusCode != http.StatusBadRequest || !strings.Contains(page, tc.want) {
				t.Fatalf("status = %d, want 400 with %q", res.StatusCode, tc.want)
			}
			if strings.Contains(page, "data-replace") || strings.Contains(page, "secret-mask") {
				t.Fatal("the create form shows a secret as saved")
			}
			if channels, _ := st.Channels(ctx); len(channels) != 0 {
				t.Fatalf("the error path saved %d channels", len(channels))
			}
			// Each secret shows only in its own input: not in the error,
			// not in the inputs of other types.
			if n := strings.Count(page, "SECRET"); n != len(tc.secrets) {
				t.Fatalf("secrets show %d times on the page, want %d", n, len(tc.secrets))
			}

			// Submit again as the browser does: the inputs as the page
			// shows them, with the error fixed.
			again := url.Values{"type": {tc.typ}}
			for k, v := range tc.form {
				again[k] = v
			}
			for k, v := range tc.fix {
				again[k] = v
			}
			for key, secret := range tc.secrets {
				v, ok := formInputValue(page, tc.typ+"_"+key)
				if !ok || v != secret {
					t.Fatalf("input %s_%s does not keep the typed secret", tc.typ, key)
				}
				again.Set(tc.typ+"_"+key, v)
			}
			res = postForm(t, c, ts.URL+"/notifications/new", again)
			if b := body(t, res); res.StatusCode != http.StatusSeeOther || strings.Contains(b, "SECRET") || strings.Contains(res.Header.Get("Location"), "SECRET") {
				t.Fatalf("second submit = %d, want 303 without a secret", res.StatusCode)
			}
			channels, _ := st.Channels(ctx)
			if len(channels) != 1 {
				t.Fatalf("saved %d channels, want 1", len(channels))
			}
			for key, secret := range tc.secrets {
				if channels[0].Config[key] != secret {
					t.Errorf("saved %s does not match the typed secret", key)
				}
			}
			// After the save, no page shows the secret again.
			for _, path := range []string{"/notifications", "/notifications/" + strconv.FormatInt(channels[0].ID, 10) + "/edit"} {
				if strings.Contains(body(t, get(t, c, ts.URL+path)), "SECRET") {
					t.Errorf("%s shows a saved secret", path)
				}
			}
			if err := st.DeleteChannel(ctx, channels[0].ID); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, typ := range notify.Types {
		for _, f := range notify.Fields(typ.Type) {
			if f.Secret && !covered[typ.Type] {
				t.Errorf("no test for the secret fields of %s", typ.Type)
				break
			}
		}
	}

	// Edit: a stored secret that the user leaves empty shows as saved. A
	// new secret that the user types stays in the form after an error.
	ch := &store.Channel{Type: store.ChannelTelegram, Name: "Bot", Enabled: true, Config: map[string]string{"token": "123:OLDSECRET", "chat_id": "42"}}
	if err := st.CreateChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}
	edit := ts.URL + "/notifications/" + strconv.FormatInt(ch.ID, 10) + "/edit"
	res := postForm(t, c, edit, url.Values{"telegram_token": {""}, "telegram_chat_id": {""}})
	if page := body(t, res); res.StatusCode != http.StatusBadRequest || !strings.Contains(page, "data-replace") || strings.Contains(page, "OLDSECRET") {
		t.Fatalf("edit error with an empty secret = %d, want 400 with the stored secret shown as saved", res.StatusCode)
	}
	res = postForm(t, c, edit, url.Values{"telegram_token": {"456:NEWSECRET"}, "telegram_chat_id": {""}})
	page := body(t, res)
	if v, _ := formInputValue(page, "telegram_token"); res.StatusCode != http.StatusBadRequest || strings.Contains(page, "data-replace") || v != "456:NEWSECRET" {
		t.Fatalf("edit error with a new secret = %d, want 400 with the new secret kept in its input", res.StatusCode)
	}
	res = postForm(t, c, edit, url.Values{"telegram_token": {"456:NEWSECRET"}, "telegram_chat_id": {"42"}})
	res.Body.Close()
	if got, _ := st.Channel(ctx, ch.ID); res.StatusCode != http.StatusSeeOther || got.Config["token"] != "456:NEWSECRET" {
		t.Fatalf("edit = %d, the new token was not saved", res.StatusCode)
	}
	if strings.Contains(body(t, get(t, c, edit)), "SECRET") {
		t.Fatal("the edit form shows the saved secret")
	}

	// No secret reaches the logs.
	if strings.Contains(logs.String(), "SECRET") {
		t.Fatal("a secret reached the logs")
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
