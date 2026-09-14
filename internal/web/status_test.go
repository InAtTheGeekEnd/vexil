package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/brand"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

func TestStatusPage(t *testing.T) {
	s, st := newTestServer(t, Options{Brand: brand.Brand{Name: "Acme Watch", Accent: "#4F46E5", PoweredBy: true}})
	setPassword(t, st)
	ctx := context.Background()

	rec := httptestRecord(s, http.MethodGet, "/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("empty status = %d", rec.Code)
	}
	b := rec.Body.String()
	if !strings.Contains(b, "No monitors are on this page yet.") || !strings.Contains(b, "Acme Watch") {
		t.Fatalf("empty page: %s", b)
	}

	pub := &store.Monitor{Name: "Website", Type: store.TypeHTTP, Target: "https://secret-host.example/path", IntervalS: 60, Public: true}
	priv := &store.Monitor{Name: "Database", Type: store.TypeTCP, Target: "db.internal.example:5432", IntervalS: 60}
	for _, m := range []*store.Monitor{pub, priv} {
		if err := st.CreateMonitor(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.OpenIncident(ctx, pub.ID, time.Now().Add(-2*time.Hour), "HTTP 503"); err != nil {
		t.Fatal(err)
	}
	if err := st.CloseIncident(ctx, pub.ID, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.OpenIncident(ctx, priv.ID, time.Now().Add(-time.Hour), "connection refused"); err != nil {
		t.Fatal(err)
	}

	rec = httptestRecord(s, http.MethodGet, "/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	b = rec.Body.String()
	for _, want := range []string{"Website", "HTTP 503", `js/status.js`, `<main class="container page page-narrow" data-status>`, "Powered by " + brand.ProductName, "<title>Status · Acme Watch</title>", "uptime-90"} {
		if !strings.Contains(b, want) {
			t.Errorf("status page lacks %q", want)
		}
	}
	// The footer credits the product, not the renamed brand.
	for _, leak := range []string{"http-equiv", "Database", "secret-host", "db.internal", "connection refused", "Log out", "/monitors/", "Powered by Acme Watch"} {
		if strings.Contains(b, leak) {
			t.Errorf("status page shows %q", leak)
		}
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatal("status page lacks the security headers")
	}
}

func TestBadge(t *testing.T) {
	s, st := newTestServer(t, Options{})
	ctx := context.Background()
	pub := &store.Monitor{Name: "API <3", Type: store.TypeHTTP, Target: "https://a.example", IntervalS: 60, Public: true}
	priv := &store.Monitor{Name: "Private", Type: store.TypeHTTP, Target: "https://b.example", IntervalS: 60}
	for _, m := range []*store.Monitor{pub, priv} {
		if err := st.CreateMonitor(ctx, m); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptestRecord(s, http.MethodGet, "/badge/1.svg")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("badge = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=60" {
		t.Fatalf("badge cache = %q", cc)
	}
	b := rec.Body.String()
	if !strings.Contains(b, "API &lt;3") || strings.Contains(b, "API <3") || !strings.Contains(b, ">pending<") {
		t.Fatalf("badge body: %s", b)
	}

	// A private monitor and a missing one give the same answer.
	private := httptestRecord(s, http.MethodGet, "/badge/2.svg")
	missing := httptestRecord(s, http.MethodGet, "/badge/999.svg")
	if private.Code != http.StatusNotFound || missing.Code != http.StatusNotFound {
		t.Fatalf("private = %d, missing = %d, want 404", private.Code, missing.Code)
	}
	if private.Body.String() != missing.Body.String() || private.Header().Get("Content-Type") != missing.Header().Get("Content-Type") {
		t.Fatal("private and missing badges differ")
	}
	for _, path := range []string{"/badge/1", "/badge/1.png", "/badge/x.svg"} {
		if rec := httptestRecord(s, http.MethodGet, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", path, rec.Code)
		}
	}
}

func TestBadgeSVG(t *testing.T) {
	svg := badgeSVG("api", "up", "#16A34A")
	for _, want := range []string{`<svg xmlns="http://www.w3.org/2000/svg"`, `fill="#16A34A"`, `>api<`, `>up<`, `aria-label="api: up"`} {
		if !strings.Contains(svg, want) {
			t.Errorf("badge lacks %s", want)
		}
	}
	long := strings.Repeat("n", 60)
	svg = badgeSVG(long, "up", "#16A34A")
	if !strings.Contains(svg, `aria-label="`+long+`: up"`) || strings.Contains(svg, ">"+long+"<") || !strings.Contains(svg, "…<") {
		t.Errorf("long badge label is not shortened: %s", svg)
	}
}

// TestStatusIcon loads the public status page as a visitor who is not
// logged in. The tab icon and the title count public monitors only.
func TestStatusIcon(t *testing.T) {
	tests := []struct {
		name                    string
		privateDown, publicDown bool
		down                    bool
	}{
		{"private monitor down", true, false, false},
		{"public monitor down", false, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, st, _ := seedServer(t, seed{name: "Private", down: tt.privateDown}, seed{name: "Public", down: tt.publicDown, public: true})
			setPassword(t, st)
			ts := httptest.NewServer(s)
			t.Cleanup(ts.Close)
			b := s.currentBrand()
			icon, title := b.FaviconUpURL, "<title>Status · "+b.Name+"</title>"
			if tt.down {
				icon, title = b.FaviconDownURL, "<title>(1) Status · "+b.Name+"</title>"
			}
			page := body(t, get(t, ts.Client(), ts.URL+"/status"))
			if want := `<link rel="icon" href="` + icon + `#`; !strings.Contains(page, want) {
				t.Errorf("status page lacks %q", want)
			}
			if !strings.Contains(page, title) {
				t.Errorf("status page lacks %q", title)
			}
			if res := get(t, ts.Client(), ts.URL+icon); res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/svg+xml" {
				t.Errorf("icon for a visitor = %d %q", res.StatusCode, res.Header.Get("Content-Type"))
			}
		})
	}
}
