//go:build chrome

package web

import (
	"strconv"
	"testing"
)

// TestNameAutofillInChrome types targets into the monitor form. The name
// field fills with the host: a bare IPv6 address stays whole, and a scheme,
// a user, a port or brackets go.
func TestNameAutofillInChrome(t *testing.T) {
	path := requireChrome(t)
	s, st, _ := seedServer(t)
	ts, _ := browserFixture(t, s, st)
	page, session := startPage(t, path, "")
	loadPage(t, page, session, ts.URL+"/monitors/new")

	const fill = `(function (field, value) {
  var input = document.querySelector('#monitor-form input[name="' + field + '"]');
  input.value = value;
  input.dispatchEvent(new Event("input", { bubbles: true }));
  return document.querySelector('#monitor-form input[name="name"]').value;
})`
	tests := []struct{ field, value, want string }{
		{"host", "2001:db8::1", "2001:db8::1"},
		{"host", "::1", "::1"},
		{"host", "[2001:db8::1]:5432", "2001:db8::1"},
		{"host", "db.example.com:5432", "db.example.com"},
		{"host", "192.0.2.7", "192.0.2.7"},
		{"url", "https://user@example.com:8443/health", "example.com"},
		{"url", "https://[2001:db8::1]:443/", "2001:db8::1"},
		{"hostname", "example.com", "example.com"},
	}
	for _, tt := range tests {
		var got string
		page.eval(session, fill+"("+strconv.Quote(tt.field)+", "+strconv.Quote(tt.value)+")", &got)
		if got != tt.want {
			t.Errorf("%s %q: name = %q, want %q", tt.field, tt.value, got, tt.want)
		}
	}
}
