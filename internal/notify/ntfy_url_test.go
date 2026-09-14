package notify

import (
	"strings"
	"testing"

	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// TestNtfyURLCredentials checks a topic URL with a user name and password.
// ntfy sends the URL without them, so the form refuses it and asks for an
// access token. A channel saved before this check must not show them in the
// channel list.
func TestNtfyURLCredentials(t *testing.T) {
	withUser := store.Channel{Type: store.ChannelNtfy, Config: map[string]string{"url": "https://alice:s3cret@ntfy.example.com/alerts"}}
	if msg := Validate(withUser)["url"]; !strings.Contains(msg, "access token") {
		t.Fatalf("url error = %q, want a request for an access token", msg)
	}
	if got := Summary(withUser); got != "ntfy.example.com/alerts" {
		t.Fatalf("Summary = %q, want the host and topic only", got)
	}

	plain := store.Channel{Type: store.ChannelNtfy, Config: map[string]string{"url": "https://ntfy.example.com/alerts", "token": "tk_abc"}}
	if errs := Validate(plain); len(errs) != 0 {
		t.Fatalf("errors for a plain topic URL = %v, want none", errs)
	}
	if got := Summary(plain); got != "ntfy.example.com/alerts" {
		t.Fatalf("Summary = %q, want ntfy.example.com/alerts", got)
	}
}
