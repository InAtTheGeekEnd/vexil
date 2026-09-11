package notify

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/check"
	"github.com/InAtTheGeekEnd/vexil/internal/engine"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

func newTestService(t *testing.T) (*Service, *store.Store, *[]time.Duration) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := NewService(st, Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Brand: "vexil", BaseURL: "https://status.example.com"})
	slept := &[]time.Duration{}
	s.sleep = func(ctx context.Context, d time.Duration) bool {
		*slept = append(*slept, d)
		return true
	}
	return s, st, slept
}

func TestNotifyRetriesAndRecordsFailure(t *testing.T) {
	s, st, slept := newTestService(t)
	ctx := context.Background()
	m := &store.Monitor{Name: "API", Type: store.TypeHTTP, Target: "https://api.example.com"}
	if err := st.CreateMonitor(ctx, m); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	var failUntil int32 = 2 // the first two attempts fail
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= failUntil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		io.WriteString(w, "ok")
	}))
	defer ts.Close()
	hook := &store.Channel{Type: store.ChannelWebhook, Name: "Hook", Config: map[string]string{"url": ts.URL + "/hook?key=SECRET"}, Enabled: true}
	off := &store.Channel{Type: store.ChannelWebhook, Name: "Off", Config: map[string]string{"url": ts.URL + "/off"}, Enabled: false}
	for _, c := range []*store.Channel{hook, off} {
		if err := st.CreateChannel(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	// Two failures, then success: two retries, no stored error.
	s.Notify(ctx, engine.Event{MonitorID: m.ID, Alert: engine.AlertDown, At: testAt, Result: check.Result{Error: "HTTP 503"}})
	s.Wait()
	if calls.Load() != 3 || len(*slept) != 2 || (*slept)[0] != 5*time.Second || (*slept)[1] != 30*time.Second {
		t.Fatalf("calls = %d, sleeps = %v", calls.Load(), *slept)
	}
	if c, _ := st.Channel(ctx, hook.ID); c.LastError != "" {
		t.Fatalf("last error after success = %q", c.LastError)
	}

	// Four failures: the last error is stored, without the secret.
	calls.Store(0)
	failUntil = 100
	*slept = nil
	s.Notify(ctx, engine.Event{MonitorID: m.ID, Alert: engine.AlertUp, At: testAt})
	s.Wait()
	if calls.Load() != 4 || len(*slept) != 3 || (*slept)[2] != 2*time.Minute {
		t.Fatalf("calls = %d, sleeps = %v", calls.Load(), *slept)
	}
	c, _ := st.Channel(ctx, hook.ID)
	if c.LastError != "HTTP 502" || c.LastErrorAt.IsZero() {
		t.Fatalf("last error = %q at %v", c.LastError, c.LastErrorAt)
	}

	// A success clears the stored error.
	failUntil = 0
	s.Notify(ctx, engine.Event{MonitorID: m.ID, Alert: engine.AlertUp, At: testAt})
	s.Wait()
	if c, _ = st.Channel(ctx, hook.ID); c.LastError != "" {
		t.Fatalf("last error not cleared: %q", c.LastError)
	}

	// AlertNone sends nothing.
	calls.Store(0)
	s.Notify(ctx, engine.Event{MonitorID: m.ID, Alert: engine.AlertNone})
	s.Wait()
	if calls.Load() != 0 {
		t.Fatal("AlertNone was delivered")
	}
}

func TestMessageFromEvent(t *testing.T) {
	s, st, _ := newTestService(t)
	ctx := context.Background()
	m := &store.Monitor{Name: "API", Type: store.TypeHTTP, Target: "https://api.example.com"}
	if err := st.CreateMonitor(ctx, m); err != nil {
		t.Fatal(err)
	}
	if _, err := st.OpenIncident(ctx, m.ID, testAt.Add(-4*time.Minute-12*time.Second), "HTTP 503"); err != nil {
		t.Fatal(err)
	}
	if err := st.CloseIncident(ctx, m.ID, testAt); err != nil {
		t.Fatal(err)
	}
	up, err := s.message(ctx, engine.Event{MonitorID: m.ID, Alert: engine.AlertUp, At: testAt})
	if err != nil {
		t.Fatal(err)
	}
	if up.Kind != KindUp || up.DownFor != 4*time.Minute+12*time.Second || up.URL != "https://status.example.com/monitors/"+strconv.FormatInt(m.ID, 10) || up.Brand != "vexil" {
		t.Fatalf("up message = %+v", up)
	}
	expiry := testAt.Add(10 * 24 * time.Hour)
	cert, err := s.message(ctx, engine.Event{MonitorID: m.ID, Alert: engine.AlertCert, At: testAt, Result: check.Result{OK: true, CertExpiry: expiry}})
	if err != nil {
		t.Fatal(err)
	}
	if cert.Kind != KindCert || !cert.CertExpiry.Equal(expiry) || !strings.Contains(cert.Title(), "expires in 10 days") {
		t.Fatalf("cert message = %+v", cert)
	}
}

func TestTest(t *testing.T) {
	s, _, _ := newTestService(t)
	ts, cap := newCapture(t)
	c := store.Channel{Type: store.ChannelSlack, Config: map[string]string{"url": ts.URL + "/services/SECRET"}}
	if err := s.Test(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cap.body, "Test message from vexil") {
		t.Fatalf("body = %s", cap.body)
	}
	cap.status = http.StatusNotFound
	err := s.Test(context.Background(), c)
	if err == nil || err.Error() != "HTTP 404" {
		t.Fatalf("err = %v", err)
	}
}
