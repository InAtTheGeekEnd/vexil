package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/brand"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

func TestStatusPage(t *testing.T) {
	s, st := newTestServer(t, Options{Brand: brand.Brand{Name: "Acme Watch", Accent: "#6366F1", PoweredBy: true}})
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
	for _, want := range []string{"Website", "HTTP 503", `content="60"`, "Powered by Acme Watch", "<title>Status · Acme Watch</title>", "uptime-90"} {
		if !strings.Contains(b, want) {
			t.Errorf("status page lacks %q", want)
		}
	}
	for _, leak := range []string{"Database", "secret-host", "db.internal", "connection refused", "Log out", "/monitors/"} {
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
}
