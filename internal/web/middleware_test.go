package web

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAccessLogHidesPushToken checks that the access log does not write the
// secret token of a push URL, for both methods and with or without a
// monitor for the token. Other paths are logged as they are.
func TestAccessLogHidesPushToken(t *testing.T) {
	tests := []struct {
		method, path string
		secret       string // must not be in the log
		wantPath     string
	}{
		{http.MethodGet, "/push/tok_9f8e7d6c5b4a", "tok_9f8e7d6c5b4a", "path=/push/[redacted]"},
		{http.MethodPost, "/push/tok_9f8e7d6c5b4a", "tok_9f8e7d6c5b4a", "path=/push/[redacted]"},
		{http.MethodGet, "/status", "", "path=/status"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			logs := &lockedBuffer{}
			s, _ := newTestServer(t, Options{Log: slog.New(slog.NewTextHandler(logs, nil))})
			req := httptest.NewRequest(tt.method, tt.path, nil)
			s.ServeHTTP(httptest.NewRecorder(), req)
			out := logs.String()
			if !strings.Contains(out, "msg=request") || !strings.Contains(out, tt.wantPath) {
				t.Fatalf("log = %q, want a request line with %s", out, tt.wantPath)
			}
			if tt.secret != "" && strings.Contains(out, tt.secret) {
				t.Fatalf("log = %q, it contains the push token", out)
			}
		})
	}
}
