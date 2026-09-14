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

// fakePushover is a Pushover API that records each request as one line.
type fakePushover struct {
	mu           sync.Mutex
	calls        []string
	failCancel   bool   // every cancel answers HTTP 500
	failMessages int    // the next n messages answer HTTP 500
	onMessage    func() // runs once, before the answer to the next message
}

// Requests as fakePushover records them. {tag} stands for an incident tag.
const (
	downReq   = "message priority=2 tags={tag}"
	plainReq  = "message priority=0 tags=<nil>"
	cancelReq = "cancel /1/receipts/cancel_by_tag/{tag}.json token=aAPPTOKEN"
)

func newFakePushover(t *testing.T) *fakePushover {
	t.Helper()
	f := &fakePushover{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path == "/1/messages.json" {
			f.mu.Lock()
			hook := f.onMessage
			f.onMessage = nil
			f.mu.Unlock()
			if hook != nil {
				hook()
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		fail := f.failCancel
		if r.URL.Path == "/1/messages.json" {
			f.calls = append(f.calls, fmt.Sprintf("message priority=%v tags=%v", body["priority"], body["tags"]))
			fail = f.failMessages > 0
			if fail {
				f.failMessages--
			}
		} else {
			f.calls = append(f.calls, fmt.Sprintf("cancel %s token=%v", r.URL.Path, body["token"]))
		}
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		io.WriteString(w, `{"status":1,"request":"x"}`)
	}))
	t.Cleanup(ts.Close)
	old := pushoverAPI
	pushoverAPI = ts.URL
	t.Cleanup(func() { pushoverAPI = old })
	return f
}

// take returns the requests so far, sorted, and forgets them. Alerts and
// cancels run at the same time, so their order is not fixed.
func (f *fakePushover) take() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// A DOWN, UP, DOWN sequence: the first DOWN alert is on its way to Pushover
// when the first incident closes and the second outage starts. Its delivery
// must cancel with its own tag (SPEC.md section 7.2).
func TestPushoverLateCancelAfterNewOutage(t *testing.T) {
	s, st, slept := newTestService(t)
	api := newFakePushover(t)
	ctx := context.Background()
	mon := &store.Monitor{Name: "API", Type: store.TypeHTTP, Target: "https://api.example.com"}
	if err := st.CreateMonitor(ctx, mon); err != nil {
		t.Fatal(err)
	}
	ch := &store.Channel{Type: store.ChannelPushover, Name: "Phone", Enabled: true, Config: map[string]string{
		"user": "uUSERKEY", "token": "aAPPTOKEN", "repeat": "1", "retry": "1", "expire": "60"}}
	if err := st.CreateChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}
	firstAt, upAt, secondAt := testAt, testAt.Add(3*time.Minute), testAt.Add(5*time.Minute)

	// While Pushover takes the first DOWN alert, the engine closes the first
	// incident and sends UP, then opens a second incident and sends DOWN.
	api.mu.Lock()
	api.onMessage = func() {
		if err := st.CloseIncident(ctx, mon.ID, upAt); err != nil {
			t.Error(err)
		}
		s.Notify(ctx, engine.Event{MonitorID: mon.ID, Alert: engine.AlertUp, At: upAt})
		if _, err := st.OpenIncident(ctx, mon.ID, secondAt, "HTTP 503"); err != nil {
			t.Error(err)
		}
		s.Notify(ctx, engine.Event{MonitorID: mon.ID, Alert: engine.AlertDown, At: secondAt, Result: check.Result{Error: "HTTP 503"}})
	}
	api.mu.Unlock()

	if _, err := st.OpenIncident(ctx, mon.ID, firstAt, "HTTP 503"); err != nil {
		t.Fatal(err)
	}
	s.Notify(ctx, engine.Event{MonitorID: mon.ID, Alert: engine.AlertDown, At: firstAt, Result: check.Result{Error: "HTTP 503"}})
	s.Wait()

	first := strings.ReplaceAll(cancelReq, "{tag}", fmt.Sprintf("m%d-%d", mon.ID, firstAt.Unix()))
	firstDown := strings.ReplaceAll(downReq, "{tag}", fmt.Sprintf("m%d-%d", mon.ID, firstAt.Unix()))
	secondDown := strings.ReplaceAll(downReq, "{tag}", fmt.Sprintf("m%d-%d", mon.ID, secondAt.Unix()))
	want := []string{
		first,      // the UP alert cancels before Pushover has the first DOWN alert
		first,      // the delivery of the first DOWN alert cancels it
		plainReq,   // the UP alert
		firstDown,  // the first DOWN alert
		secondDown, // the second DOWN alert, not cancelled
	}
	sort.Strings(want)
	if got := api.take(); got != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	if len(*slept) != 0 {
		t.Fatalf("sleeps = %v, want none", *slept)
	}
}

// A DOWN alert whose first attempt fails waits for a retry. The incident
// closes and the UP alert goes out during the wait. The retry must be
// dropped, so no DOWN alert arrives after the UP alert, and the channel
// keeps no error.
func TestDownRetryDroppedAfterRecovery(t *testing.T) {
	s, st, _ := newTestService(t)
	var logs bytes.Buffer
	s.log = slog.New(slog.NewTextHandler(&logs, nil))
	api := newFakePushover(t)
	api.mu.Lock()
	api.failMessages = 1 // the first DOWN alert needs a retry
	api.mu.Unlock()
	ctx := context.Background()
	mon := &store.Monitor{Name: "API", Type: store.TypeHTTP, Target: "https://api.example.com"}
	if err := st.CreateMonitor(ctx, mon); err != nil {
		t.Fatal(err)
	}
	ch := &store.Channel{Type: store.ChannelPushover, Name: "Phone", Enabled: true, Config: map[string]string{
		"user": "uUSERKEY", "token": "aAPPTOKEN", "repeat": "1", "retry": "1", "expire": "60"}}
	if err := st.CreateChannel(ctx, ch); err != nil {
		t.Fatal(err)
	}
	firstAt, upAt := testAt, testAt.Add(3*time.Minute)

	// While the DOWN alert waits for its retry, the engine closes the
	// incident and sends UP.
	var sleeps atomic.Int32
	s.sleep = func(ctx context.Context, d time.Duration) bool {
		if sleeps.Add(1) == 1 {
			if err := st.CloseIncident(ctx, mon.ID, upAt); err != nil {
				t.Error(err)
			}
			s.Notify(ctx, engine.Event{MonitorID: mon.ID, Alert: engine.AlertUp, At: upAt})
		}
		return true
	}

	if _, err := st.OpenIncident(ctx, mon.ID, firstAt, "HTTP 503"); err != nil {
		t.Fatal(err)
	}
	s.Notify(ctx, engine.Event{MonitorID: mon.ID, Alert: engine.AlertDown, At: firstAt, Result: check.Result{Error: "HTTP 503"}})
	s.Wait()

	want := []string{
		strings.ReplaceAll(cancelReq, "{tag}", fmt.Sprintf("m%d-%d", mon.ID, firstAt.Unix())), // the UP alert cancels
		plainReq, // the UP alert
		strings.ReplaceAll(downReq, "{tag}", fmt.Sprintf("m%d-%d", mon.ID, firstAt.Unix())), // the DOWN alert, failed attempt
	}
	sort.Strings(want)
	if got := api.take(); got != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	if n := sleeps.Load(); n != 1 {
		t.Fatalf("sleeps = %d, want 1", n)
	}
	if c, _ := st.Channel(ctx, ch.ID); c.LastError != "" {
		t.Fatalf("channel error = %q, want none", c.LastError)
	}
	if !strings.Contains(logs.String(), "DOWN alert dropped") {
		t.Fatal("the dropped retry was not logged")
	}
}

func TestPushoverCancelOnRecovery(t *testing.T) {
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
			api := newFakePushover(t)
			api.mu.Lock()
			api.failCancel = tc.failCancel
			api.mu.Unlock()
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
			if got := api.take(); got != want(tc.wantDown) {
				t.Fatalf("DOWN requests:\n%s\nwant:\n%s", got, want(tc.wantDown))
			}
			if tc.wantUp != nil {
				if err := st.CloseIncident(ctx, mon.ID, upAt); err != nil {
					t.Fatal(err)
				}
				s.Notify(ctx, engine.Event{MonitorID: mon.ID, Alert: engine.AlertUp, At: upAt})
				s.Wait()
				if got := api.take(); got != want(tc.wantUp) {
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
