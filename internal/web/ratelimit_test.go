package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestLoginLimitIPv6Prefix sends wrong passwords from different addresses
// in one IPv6 /64. They share one limit, so the sixth attempt is refused. A
// client in another /64 and an IPv4 client keep their own limits.
func TestLoginLimitIPv6Prefix(t *testing.T) {
	s, st := newTestServer(t, Options{})
	setPassword(t, st)
	login := func(remoteAddr string) int {
		t.Helper()
		form := url.Values{"password": {"wrong wrong wrong"}}
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = remoteAddr
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := 1; i <= 6; i++ {
		want := http.StatusUnauthorized
		if i > 5 {
			want = http.StatusTooManyRequests
		}
		addr := fmt.Sprintf("[2001:db8:1:2::%x]:1234", i*0x1111)
		if code := login(addr); code != want {
			t.Fatalf("attempt %d from %s = %d, want %d", i, addr, code, want)
		}
	}
	for _, other := range []string{"[2001:db8:1:3::1]:1234", "203.0.113.9:1234"} {
		if code := login(other); code != http.StatusUnauthorized {
			t.Fatalf("attempt from %s = %d, want 401", other, code)
		}
	}
}
