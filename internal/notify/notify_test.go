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
			m:    Message{Kind: KindTest, Brand: "vexil", At: testAt},
			want: "🔔 Test message from vexil\nThis channel works.",
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
		{"email ok", store.Channel{Type: store.ChannelEmail, Config: map[string]string{"host": "smtp.example.com", "port": "587", "from": "a@example.com", "to": "b@example.com"}}, nil},
		{"email bad", store.Channel{Type: store.ChannelEmail, Config: map[string]string{"host": "smtp.example.com", "port": "0", "from": "nope", "to": "b@example.com"}}, []string{"port", "from"}},
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
	if got := Summary(store.Channel{Type: store.ChannelSlack, Config: map[string]string{"url": secret}}); strings.Contains(got, "SECRETTOKEN") || !strings.Contains(got, "hooks.slack.com") {
		t.Fatalf("Summary = %q", got)
	}
}
