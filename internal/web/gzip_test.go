package web

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompress(t *testing.T) {
	s, _ := newTestServer(t, Options{})
	tests := []struct {
		name   string
		path   string
		accept string
		want   bool
	}{
		{"html page", "/setup", "gzip, deflate, br", true},
		{"css", "/static/css/app.css", "gzip", true},
		{"badge svg 404 stays plain", "/badge/1.svg", "gzip", false},
		{"font is already compressed", "/static/fonts/InterVariable.woff2", "gzip", false},
		{"client without gzip", "/setup", "", false},
		{"json readyz", "/readyz", "gzip", true},
	}
	for _, tc := range tests {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		if tc.accept != "" {
			req.Header.Set("Accept-Encoding", tc.accept)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		got := rec.Header().Get("Content-Encoding") == "gzip"
		if got != tc.want {
			t.Errorf("%s: gzip = %v, want %v (status %d, type %q)", tc.name, got, tc.want, rec.Code, rec.Header().Get("Content-Type"))
		}
		if !got {
			continue
		}
		zr, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		body, err := io.ReadAll(zr)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(body) <= rec.Body.Len() && rec.Code == http.StatusOK && len(body) > 1000 {
			t.Errorf("%s: compressed %d bytes is not smaller than %d", tc.name, rec.Body.Len(), len(body))
		}
		if tc.path == "/setup" && !strings.Contains(string(body), "<form") {
			t.Errorf("%s: body is not the page", tc.name)
		}
	}
}
