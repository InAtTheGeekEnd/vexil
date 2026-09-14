package notify

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

var (
	testAt  = time.Date(2026, 9, 11, 14, 2, 0, 0, time.UTC)
	testMon = store.Monitor{ID: 12, Name: "API", Type: store.TypeHTTP, Target: "https://api.example.com/health"}
)

func TestMessageText(t *testing.T) {
	tests := []struct {
		name string
		m    Message
		want string
	}{
		{
			name: "down",
			m:    Message{Kind: KindDown, Monitor: testMon, Reason: "HTTP 503", At: testAt, URL: "https://status.example.com/monitors/12"},
			want: "🔴 API is down\nReason: HTTP 503\nSince: 14:02 UTC\nhttps://status.example.com/monitors/12",
		},
		{
			name: "up",
			m:    Message{Kind: KindUp, Monitor: testMon, At: testAt, DownFor: 4*time.Minute + 12*time.Second, URL: "https://status.example.com/monitors/12"},
			want: "🟢 API is up again\nDown for: 4m 12s\nhttps://status.example.com/monitors/12",
		},
		{
			name: "up without a known incident and no base URL",
			m:    Message{Kind: KindUp, Monitor: testMon, At: testAt},
			want: "🟢 API is up again",
		},
		{
			name: "cert",
			m:    Message{Kind: KindCert, Monitor: testMon, At: testAt, CertExpiry: testAt.Add(14 * 24 * time.Hour)},
			want: "🟡 The TLS certificate for api.example.com expires in 14 days (25 Sep 2026)",
		},
		{
			name: "cert in one day",
			m:    Message{Kind: KindCert, Monitor: testMon, At: testAt, CertExpiry: testAt.Add(26 * time.Hour)},
			want: "🟡 The TLS certificate for api.example.com expires in 1 day (12 Sep 2026)",
		},
		{
			name: "test",
			m:    Message{Kind: KindTest, Brand: "Acme Watch", At: testAt},
			want: "🔔 Test message from Acme Watch\nThis channel works.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.Text(); got != tc.want {
				t.Errorf("Text() =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestDownFor(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{12 * time.Second, "0m 12s"},
		{4*time.Minute + 12*time.Second, "4m 12s"},
		{time.Hour + 3*time.Minute, "1h 3m"},
		{2*24*time.Hour + 5*time.Hour, "2d 5h"},
	}
	for _, tc := range tests {
		if got := downFor(tc.d); got != tc.want {
			t.Errorf("downFor(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		channel store.Channel
		wantErr []string // keys that must have an error
	}{
		{"unknown type", store.Channel{Type: "pager"}, []string{"type"}},
		{"slack ok", store.Channel{Type: store.ChannelSlack, Config: map[string]string{"url": "https://hooks.slack.com/services/T/B/x"}}, nil},
		{"slack bad url", store.Channel{Type: store.ChannelSlack, Config: map[string]string{"url": "hooks.slack.com/x"}}, []string{"url"}},
		{"slack missing", store.Channel{Type: store.ChannelSlack, Config: map[string]string{}}, []string{"url"}},
		{"telegram", store.Channel{Type: store.ChannelTelegram, Config: map[string]string{"token": "1:abc"}}, []string{"chat_id"}},
		{"ntfy no token is fine", store.Channel{Type: store.ChannelNtfy, Config: map[string]string{"url": "https://ntfy.sh/t"}}, nil},
		{"pushover ok", store.Channel{Type: store.ChannelPushover, Config: map[string]string{"user": "u", "token": "a"}}, nil},
		{"pushover missing keys", store.Channel{Type: store.ChannelPushover, Config: map[string]string{}}, []string{"user", "token"}},
		{"pushover repeat off skips the times", store.Channel{Type: store.ChannelPushover, Config: map[string]string{"user": "u", "token": "a", "retry": "0"}}, nil},
		{"pushover repeat checks the times", store.Channel{Type: store.ChannelPushover, Config: map[string]string{"user": "u", "token": "a", "repeat": "1", "retry": "0", "expire": "181"}}, []string{"retry", "expire"}},
		{"pushover repeat needs the times", store.Channel{Type: store.ChannelPushover, Config: map[string]string{"user": "u", "token": "a", "repeat": "1"}}, []string{"retry", "expire"}},
		{"pushover repeat ok", store.Channel{Type: store.ChannelPushover, Config: map[string]string{"user": "u", "token": "a", "repeat": "1", "retry": "60", "expire": "180"}}, nil},
		{"email ok", store.Channel{Type: store.ChannelEmail, Config: map[string]string{"host": "smtp.example.com", "port": "587", "from": "a@example.com", "to": "b@example.com"}}, nil},
		{"email bad", store.Channel{Type: store.ChannelEmail, Config: map[string]string{"host": "smtp.example.com", "port": "0", "from": "nope", "to": "b@example.com"}}, []string{"port", "from"}},
		{"email display names", store.Channel{Type: store.ChannelEmail, Config: map[string]string{"host": "smtp.example.com", "port": "587", "from": "Alerts Bot <alerts@example.com>", "to": "\"Ops, Team\" <ops@example.com>"}}, nil},
		{"email two recipients", store.Channel{Type: store.ChannelEmail, Config: map[string]string{"host": "smtp.example.com", "port": "587", "from": "a@example.com", "to": "b@example.com, c@example.com"}}, []string{"to"}},
		{"email unclosed bracket", store.Channel{Type: store.ChannelEmail, Config: map[string]string{"host": "smtp.example.com", "port": "587", "from": "Alerts <alerts@example.com", "to": "b@example.com"}}, []string{"from"}},
		{"email no domain", store.Channel{Type: store.ChannelEmail, Config: map[string]string{"host": "smtp.example.com", "port": "587", "from": "a@example.com", "to": "ops"}}, []string{"to"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := Validate(tc.channel)
			if len(errs) != len(tc.wantErr) {
				t.Fatalf("errors = %v, want keys %v", errs, tc.wantErr)
			}
			for _, k := range tc.wantErr {
				if errs[k] == "" {
					t.Errorf("no error for %q: %v", k, errs)
				}
			}
		})
	}
}

func TestValidateNumberMessage(t *testing.T) {
	email := map[string]string{"host": "smtp.example.com", "from": "a@example.com", "to": "b@example.com"}
	pushover := map[string]string{"user": "u", "token": "a", "repeat": "1", "retry": "1", "expire": "60"}
	with := func(base map[string]string, key, value string) map[string]string {
		out := map[string]string{key: value}
		for k, v := range base {
			if k != key {
				out[k] = v
			}
		}
		return out
	}
	tests := []struct {
		name    string
		channel store.Channel
		key     string
		want    string
	}{
		{"email port", store.Channel{Type: store.ChannelEmail, Config: with(email, "port", "70000")}, "port", "Enter a number between 1 and 65535."},
		{"pushover retry interval", store.Channel{Type: store.ChannelPushover, Config: with(pushover, "retry", "61")}, "retry", "Enter a number between 1 and 60."},
		{"pushover expiry time", store.Channel{Type: store.ChannelPushover, Config: with(pushover, "expire", "181")}, "expire", "Enter a number between 1 and 180."},
		{"not a number", store.Channel{Type: store.ChannelPushover, Config: with(pushover, "expire", "soon")}, "expire", "Enter a number between 1 and 180."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := Validate(tc.channel)
			if len(errs) != 1 || errs[tc.key] != tc.want {
				t.Fatalf("errors = %v, want %s: %q", errs, tc.key, tc.want)
			}
		})
	}
}

// A number field without its own range would get the message "between 0
// and 0". Every number field must set Min and Max.
func TestNumberFieldsHaveARange(t *testing.T) {
	for _, typ := range Types {
		for _, f := range Fields(typ.Type) {
			if f.Type == "number" && f.Max <= f.Min {
				t.Errorf("%s field %q has no range: Min %d, Max %d", typ.Type, f.Key, f.Min, f.Max)
			}
		}
	}
}

func TestRedaction(t *testing.T) {
	secret := "https://hooks.slack.com/services/T000/B000/SECRETTOKEN"
	inner := errors.New("dial tcp 203.0.113.9:443: connect: connection refused")
	err := describe(&url.Error{Op: "Post", URL: secret, Err: inner})
	if got := err.Error(); strings.Contains(got, "SECRETTOKEN") || strings.Contains(got, "hooks.slack.com") {
		t.Fatalf("describe leaked the URL: %q", got)
	}
	c := store.Channel{Type: store.ChannelTelegram, Config: map[string]string{"token": "123:ABCDEF", "chat_id": "42"}}
	if got := Redact("Post https://api.telegram.org/bot123:ABCDEF/sendMessage: boom", Secrets(c)); strings.Contains(got, "ABCDEF") {
		t.Fatalf("Redact left the token: %q", got)
	}
	p := store.Channel{Type: store.ChannelPushover, Config: map[string]string{"user": "uUSERKEY123", "token": "aAPPTOKEN456", "repeat": "1", "retry": "1", "expire": "60"}}
	if got := Redact("user uUSERKEY123 token aAPPTOKEN456", Secrets(p)); strings.Contains(got, "USERKEY") || strings.Contains(got, "APPTOKEN") {
		t.Fatalf("Redact left a Pushover key: %q", got)
	}
	if got := Summary(p); strings.Contains(got, "USERKEY") || got != "DOWN alerts repeat until acknowledged" {
		t.Fatalf("Pushover summary = %q", got)
	}
	if got := Summary(store.Channel{Type: store.ChannelSlack, Config: map[string]string{"url": secret}}); strings.Contains(got, "SECRETTOKEN") || !strings.Contains(got, "hooks.slack.com") {
		t.Fatalf("Summary = %q", got)
	}
}
