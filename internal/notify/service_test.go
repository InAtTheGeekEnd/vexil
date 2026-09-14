package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	s := NewService(st, Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Brand: "Acme Watch", BaseURL: "https://status.example.com"})
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

func TestPushoverDeliveryRetriesAndRedacts(t *testing.T) {
	s, st, slept := newTestService(t)
	ctx := context.Background()
	m := &store.Monitor{Name: "API", Type: store.TypeHTTP, Target: "https://api.example.com"}
	if err := st.CreateMonitor(ctx, m); err != nil {
		t.Fatal(err)
	}
	ts, cap := newCapture(t)
	old := pushoverAPI
	pushoverAPI = ts.URL
	t.Cleanup(func() { pushoverAPI = old })

	// The fake API rejects every attempt and echoes the user key back.
	const user = "uQiRzpo4DXghDmr9QzzfQu27cmVRsG"
	cap.status, cap.reply = http.StatusBadRequest, `{"errors":["user `+user+` is invalid"],"status":0}`
	ch := &store.Channel{Type: store.ChannelPushover, Name: "Phone", Enabled: true, Config: map[string]string{
		"user": user, "token": "azGDORePK8gMaC0QOYAMyEEuzJnyUi", "repeat": "1", "retry": "1", "expire": "60"}}
	if err := st.CreateChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}

	s.Notify(ctx, engine.Event{MonitorID: m.ID, Alert: engine.AlertDown, At: testAt, Result: check.Result{Error: "HTTP 503"}})
	s.Wait()
	if len(*slept) != 3 {
		t.Fatalf("sleeps = %v, want 3 retries", *slept)
	}
	if body := cap.json(t); body["priority"].(float64) != 2 || body["retry"].(float64) != 60 || body["expire"].(float64) != 3600 {
		t.Fatalf("body = %v", body)
	}
	c, _ := st.Channel(ctx, ch.ID)
	if c.LastError != "HTTP 400: user •••• is invalid" {
		t.Fatalf("last error = %q", c.LastError)
	}
}

func TestPushoverCancelOnRecovery(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	failCancel := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/1/messages.json" {
			calls = append(calls, fmt.Sprintf("message priority=%v tags=%v", body["priority"], body["tags"]))
		} else {
			calls = append(calls, fmt.Sprintf("cancel %s token=%v", r.URL.Path, body["token"]))
			if failCancel {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		io.WriteString(w, `{"status":1,"request":"x"}`)
	}))
	defer ts.Close()
	old := pushoverAPI
	pushoverAPI = ts.URL
	t.Cleanup(func() { pushoverAPI = old })
	// take returns the requests so far, sorted: the UP alert and the cancel
	// run at the same time.
	take := func() string {
		mu.Lock()
		defer mu.Unlock()
		out := calls
		calls = nil
		sort.Strings(out)
		return strings.Join(out, "\n")
	}

	const (
		downReq   = "message priority=2 tags={tag}"
		plainReq  = "message priority=0 tags=<nil>"
		cancelReq = "cancel /1/receipts/cancel_by_tag/{tag}.json token=aAPPTOKEN"
	)
	tests := []struct {
		name       string
		repeat     string
		failCancel bool
		closeFirst bool     // the monitor recovers before the DOWN alert goes out
		wantDown   []string // requests for the DOWN event, sorted
		wantUp     []string // requests for the UP event, sorted; nil skips the UP event
		wantSleeps int
	}{
		{"switch on", "1", false, false, []string{downReq}, []string{cancelReq, plainReq}, 0},
		{"cancel fails", "1", true, false, []string{downReq}, []string{cancelReq, cancelReq, cancelReq, cancelReq, plainReq}, 3},
		{"switch off", "", false, false, []string{plainReq}, []string{plainReq}, 0},
		{"recovered before the DOWN alert went out", "1", false, true, []string{cancelReq, downReq}, nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, st, slept := newTestService(t)
			var logs bytes.Buffer
			s.log = slog.New(slog.NewTextHandler(&logs, nil))
			ctx := context.Background()
			mon := &store.Monitor{Name: "API", Type: store.TypeHTTP, Target: "https://api.example.com"}
			if err := st.CreateMonitor(ctx, mon); err != nil {
				t.Fatal(err)
			}
			ch := &store.Channel{Type: store.ChannelPushover, Name: "Phone", Enabled: true, Config: map[string]string{
				"user": "uUSERKEY", "token": "aAPPTOKEN", "repeat": tc.repeat, "retry": "1", "expire": "60"}}
			if err := st.CreateChannel(ctx, ch); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			failCancel, calls = tc.failCancel, nil
			mu.Unlock()
			tag := fmt.Sprintf("m%d-%d", mon.ID, testAt.Unix())
			want := func(reqs []string) string {
				return strings.ReplaceAll(strings.Join(reqs, "\n"), "{tag}", tag)
			}
			upAt := testAt.Add(3 * time.Minute)

			// The engine opens the incident before it sends the DOWN event,
			// and closes it before it sends the UP event.
			if _, err := st.OpenIncident(ctx, mon.ID, testAt, "HTTP 503"); err != nil {
				t.Fatal(err)
			}
			if tc.closeFirst {
				if err := st.CloseIncident(ctx, mon.ID, upAt); err != nil {
					t.Fatal(err)
				}
			}
			s.Notify(ctx, engine.Event{MonitorID: mon.ID, Alert: engine.AlertDown, At: testAt, Result: check.Result{Error: "HTTP 503"}})
			s.Wait()
			if got := take(); got != want(tc.wantDown) {
				t.Fatalf("DOWN requests:\n%s\nwant:\n%s", got, want(tc.wantDown))
			}
			if tc.wantUp != nil {
				if err := st.CloseIncident(ctx, mon.ID, upAt); err != nil {
					t.Fatal(err)
				}
				s.Notify(ctx, engine.Event{MonitorID: mon.ID, Alert: engine.AlertUp, At: upAt})
				s.Wait()
				if got := take(); got != want(tc.wantUp) {
					t.Fatalf("UP requests:\n%s\nwant:\n%s", got, want(tc.wantUp))
				}
			}
			if len(*slept) != tc.wantSleeps {
				t.Fatalf("sleeps = %v, want %d", *slept, tc.wantSleeps)
			}
			if c, _ := st.Channel(ctx, ch.ID); c.LastError != "" {
				t.Fatalf("channel error = %q, want none: a cancel does not set it", c.LastError)
			}
			if logged := strings.Contains(logs.String(), `msg="alert cancel failed"`); logged != tc.failCancel {
				t.Fatalf("cancel failure logged = %v, want %v", logged, tc.failCancel)
			}
		})
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
	if up.Kind != KindUp || up.DownFor != 4*time.Minute+12*time.Second || !up.Started.Equal(testAt.Add(-4*time.Minute-12*time.Second)) || up.URL != "https://status.example.com/monitors/"+strconv.FormatInt(m.ID, 10) || up.Brand != "Acme Watch" {
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
	if !strings.Contains(cap.body, "Test message from Acme Watch") {
		t.Fatalf("body = %s", cap.body)
	}
	cap.status = http.StatusNotFound
	err := s.Test(context.Background(), c)
	if err == nil || err.Error() != "HTTP 404" {
		t.Fatalf("err = %v", err)
	}
}

func TestBrandNameComesFromSettings(t *testing.T) {
	s, st, _ := newTestService(t)
	ts, cap := newCapture(t)
	if err := st.SetSetting(context.Background(), store.SettingBrandName, "Northwind Status"); err != nil {
		t.Fatal(err)
	}
	c := store.Channel{Type: store.ChannelSlack, Config: map[string]string{"url": ts.URL + "/services/SECRET"}}
	if err := s.Test(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cap.body, "Test message from Northwind Status") || !strings.Contains(cap.body, `"footer":"Northwind Status"`) {
		t.Fatalf("body = %s", cap.body)
	}
	if strings.Contains(cap.body, "Acme Watch") {
		t.Fatal("the default name was used although a brand name is saved")
	}
}
