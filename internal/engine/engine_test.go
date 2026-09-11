package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/check"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// TestTransition covers every transition in SPEC.md section 6.2.
func TestTransition(t *testing.T) {
	tests := []struct {
		name      string
		cur       State
		fails     int
		ok        bool
		wantState State
		wantFails int
		wantAlert Alert
	}{
		{"PENDING first success -> UP, no alert", Pending, 0, true, Up, 0, AlertNone},
		{"PENDING first failure stays PENDING", Pending, 0, false, Pending, 1, AlertNone},
		{"PENDING second failure -> DOWN alert", Pending, 1, false, Down, 2, AlertDown},
		{"UP success stays UP", Up, 0, true, Up, 0, AlertNone},
		{"UP first failure stays UP", Up, 0, false, Up, 1, AlertNone},
		{"UP second failure -> DOWN alert", Up, 1, false, Down, 2, AlertDown},
		{"UP failure after recovery resets count", Up, 0, false, Up, 1, AlertNone},
		{"DOWN failure stays DOWN, no repeat alert", Down, 2, false, Down, 3, AlertNone},
		{"DOWN first success -> UP alert", Down, 5, true, Up, 0, AlertUp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, f, a := transition(tt.cur, tt.fails, tt.ok)
			if s != tt.wantState || f != tt.wantFails || a != tt.wantAlert {
				t.Fatalf("got (%s, %d, %d), want (%s, %d, %d)", s, f, a, tt.wantState, tt.wantFails, tt.wantAlert)
			}
		})
	}
}

// scripted returns results in order and repeats the last one.
type scripted struct {
	mu      sync.Mutex
	results []check.Result
	i       int
}

func (s *scripted) Check(ctx context.Context) check.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.results[min(s.i, len(s.results)-1)]
	s.i++
	return r
}

// blocking never returns until the context ends.
type blocking struct{ started chan struct{} }

func (b *blocking) Check(ctx context.Context) check.Result {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return check.Result{Error: "cancelled"}
}

type recordingNotifier struct {
	mu     sync.Mutex
	events []Event
}

func (n *recordingNotifier) Notify(ctx context.Context, ev Event) {
	n.mu.Lock()
	n.events = append(n.events, ev)
	n.mu.Unlock()
}

func (n *recordingNotifier) alerts() []Alert {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []Alert
	for _, ev := range n.events {
		out = append(out, ev.Alert)
	}
	return out
}

// testScale is one second of real timing in the tests.
const testScale = 20 * time.Millisecond

type testEnv struct {
	store    *store.Store
	engine   *Engine
	notifier *recordingNotifier
	checkers map[int64]check.Checker
	mu       sync.Mutex
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	env := &testEnv{store: st, notifier: &recordingNotifier{}, checkers: map[int64]check.Checker{}}
	env.engine = New(st, Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Notifier: env.notifier})
	// Intervals are stored in seconds. Tests run every second as testScale.
	env.engine.interval = func(m store.Monitor) time.Duration {
		return time.Duration(m.IntervalS) * testScale
	}
	env.engine.retryDelay = 30 * testScale
	env.engine.maxOffset = 60 * testScale
	env.engine.pushMinExtra = 30 * testScale
	env.engine.newChecker = func(m store.Monitor) (check.Checker, error) {
		env.mu.Lock()
		defer env.mu.Unlock()
		if c, ok := env.checkers[m.ID]; ok {
			return c, nil
		}
		return &scripted{results: []check.Result{{OK: true, Latency: time.Millisecond}}}, nil
	}
	t.Cleanup(env.engine.Stop)
	return env
}

func (env *testEnv) addMonitor(t *testing.T, typ string, c check.Checker) store.Monitor {
	t.Helper()
	m := &store.Monitor{Name: "m", Type: typ, Target: "x", IntervalS: 1}
	if err := env.store.CreateMonitor(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if c != nil {
		env.mu.Lock()
		env.checkers[m.ID] = c
		env.mu.Unlock()
	}
	return *m
}

// waitFor reads events until pred is true or the deadline passes.
func waitFor(t *testing.T, ch <-chan Event, d time.Duration, pred func(Event) bool) Event {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case ev := <-ch:
			if pred(ev) {
				return ev
			}
		case <-deadline:
			t.Fatal("timed out waiting for event")
		}
	}
}

func TestEngineStateChangesAndIncidents(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()
	m := env.addMonitor(t, store.TypeHTTP, &scripted{results: []check.Result{
		{OK: true, Latency: 5 * time.Millisecond, StatusCode: 200},
		{Error: "HTTP 503", StatusCode: 503},
		{Error: "HTTP 503", StatusCode: 503},
		{Error: "timeout"},
		{OK: true, Latency: 7 * time.Millisecond, StatusCode: 200},
		{OK: true, Latency: 7 * time.Millisecond, StatusCode: 200},
	}})
	events, stop := env.engine.Hub().Subscribe(64)
	defer stop()
	if err := env.engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if st, _ := env.engine.Status(m.ID); st.State != Pending {
		t.Fatalf("initial state = %s, want PENDING", st.State)
	}

	up := waitFor(t, events, 2*time.Second, func(ev Event) bool { return ev.State == Up })
	if up.Prev != Pending || up.Alert != AlertNone {
		t.Fatalf("first UP event = %+v", up)
	}
	down := waitFor(t, events, 2*time.Second, func(ev Event) bool { return ev.State == Down })
	if down.Prev != Up || down.Alert != AlertDown || down.Result.Error != "HTTP 503" {
		t.Fatalf("DOWN event = %+v", down)
	}
	inc, err := env.store.CurrentIncident(ctx, m.ID)
	if err != nil || inc.Reason != "HTTP 503" {
		t.Fatalf("incident after DOWN = %+v, %v", inc, err)
	}
	// A third failure stays DOWN without a new alert.
	still := waitFor(t, events, 2*time.Second, func(ev Event) bool { return ev.Result.Error == "timeout" })
	if still.State != Down || still.Alert != AlertNone {
		t.Fatalf("third failure event = %+v", still)
	}
	rec := waitFor(t, events, 2*time.Second, func(ev Event) bool { return ev.State == Up && ev.Prev == Down })
	if rec.Alert != AlertUp {
		t.Fatalf("recovery event = %+v", rec)
	}
	if _, err := env.store.CurrentIncident(ctx, m.ID); err != store.ErrNotFound {
		t.Fatalf("incident still open after recovery: %v", err)
	}
	waitFor(t, events, 2*time.Second, func(ev Event) bool { return ev.State == Up && ev.Prev == Up })

	checks, err := env.store.RecentChecks(ctx, m.ID, 100)
	if err != nil || len(checks) < 5 {
		t.Fatalf("checks written = %d, %v", len(checks), err)
	}
	// Alerts: exactly one DOWN and one UP, in that order.
	time.Sleep(50 * time.Millisecond)
	if got := env.notifier.alerts(); len(got) != 2 || got[0] != AlertDown || got[1] != AlertUp {
		t.Fatalf("alerts = %v, want [DOWN UP]", got)
	}
}

func TestSlowCheckDoesNotBlockOthers(t *testing.T) {
	env := newEnv(t)
	slow := &blocking{started: make(chan struct{}, 1)}
	slowM := env.addMonitor(t, store.TypeHTTP, slow)
	fastM := env.addMonitor(t, store.TypeTCP, nil)
	events, stop := env.engine.Hub().Subscribe(64)
	defer stop()
	if err := env.engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-slow.started:
	case <-time.After(2 * time.Second):
		t.Fatal("slow check never started")
	}
	// While the slow check hangs, the fast monitor keeps reporting.
	count := 0
	deadline := time.After(2 * time.Second)
	for count < 5 {
		select {
		case ev := <-events:
			if ev.MonitorID == slowM.ID {
				t.Fatalf("slow monitor produced an event: %+v", ev)
			}
			if ev.MonitorID == fastM.ID {
				count++
			}
		case <-deadline:
			t.Fatalf("only %d fast results while a slow check ran", count)
		}
	}
	// Stop returns quickly because the slow check is cancelled.
	start := time.Now()
	env.engine.Stop()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Stop took %v", d)
	}
}

func TestPushMonitor(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()
	m := env.addMonitor(t, store.TypePush, nil)
	events, stop := env.engine.Hub().Subscribe(64)
	defer stop()
	if err := env.engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := env.engine.Push(ctx, "no-such-token"); err != store.ErrNotFound {
		t.Fatalf("unknown token err = %v", err)
	}
	if err := env.engine.Push(ctx, m.PushToken); err != nil {
		t.Fatal(err)
	}
	up := waitFor(t, events, 2*time.Second, func(ev Event) bool { return ev.MonitorID == m.ID && ev.State == Up })
	if up.Prev != Pending {
		t.Fatalf("push UP event = %+v", up)
	}
	// No further pushes: the monitor fails after the grace period, again
	// after the retry delay, and goes DOWN.
	down := waitFor(t, events, 3*time.Second, func(ev Event) bool { return ev.MonitorID == m.ID && ev.State == Down })
	if down.Result.Error != "no push received" || down.Alert != AlertDown {
		t.Fatalf("push DOWN event = %+v", down)
	}
	// A new push recovers it.
	if err := env.engine.Push(ctx, m.PushToken); err != nil {
		t.Fatal(err)
	}
	rec := waitFor(t, events, 2*time.Second, func(ev Event) bool { return ev.MonitorID == m.ID && ev.State == Up })
	if rec.Alert != AlertUp {
		t.Fatalf("push recovery event = %+v", rec)
	}
}

func TestPauseResumeDelete(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()
	m := env.addMonitor(t, store.TypeHTTP, nil)
	events, stop := env.engine.Hub().Subscribe(64)
	defer stop()
	if err := env.engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, events, 2*time.Second, func(ev Event) bool { return ev.State == Up })

	// Any state to PAUSED: user action, no alert.
	if err := env.store.SetPaused(ctx, m.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := env.engine.Reload(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	ev := waitFor(t, events, 2*time.Second, func(ev Event) bool { return ev.State == Paused })
	if ev.Prev != Up || ev.Alert != AlertNone {
		t.Fatalf("pause event = %+v", ev)
	}
	// Drain, then confirm no checks arrive while paused.
	time.Sleep(100 * time.Millisecond)
	for len(events) > 0 {
		<-events
	}
	select {
	case ev := <-events:
		t.Fatalf("event while paused: %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
	if got := env.notifier.alerts(); len(got) != 0 {
		t.Fatalf("alerts on pause: %v", got)
	}

	// Resume: PAUSED to PENDING, then UP on the first success.
	if err := env.store.SetPaused(ctx, m.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := env.engine.Reload(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if st, _ := env.engine.Status(m.ID); st.State != Pending {
		t.Fatalf("state after resume = %s, want PENDING", st.State)
	}
	waitFor(t, events, 2*time.Second, func(ev Event) bool { return ev.State == Up })

	// Delete removes the status and stops the goroutine.
	if err := env.store.DeleteMonitor(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if err := env.engine.Reload(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := env.engine.Status(m.ID); ok {
		t.Fatal("status survived delete")
	}
	env.engine.mu.Lock()
	_, running := env.engine.runners[m.ID]
	env.engine.mu.Unlock()
	if running {
		t.Fatal("runner survived delete")
	}
}

func TestInitialStatusFromDatabase(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	tests := []struct {
		name      string
		checks    []bool // oldest first
		incident  bool
		paused    bool
		wantState State
		wantFails int
	}{
		{"no checks", nil, false, false, Pending, 0},
		{"last ok", []bool{false, true}, false, false, Up, 0},
		{"one failure", []bool{true, false}, false, false, Up, 1},
		{"two failures", []bool{false, false}, false, false, Down, 2},
		{"open incident", []bool{true, false}, true, false, Down, 2},
		{"paused", []bool{false, false}, false, true, Paused, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newEnv(t)
			m := &store.Monitor{Name: "m", Type: store.TypeHTTP, Target: "x", IntervalS: 1000, Paused: tt.paused}
			if err := env.store.CreateMonitor(ctx, m); err != nil {
				t.Fatal(err)
			}
			// The seeded checks are minutes old, so the first live check is
			// due at once. A blocking checker keeps its result from replacing
			// the restored status before the assertion.
			env.mu.Lock()
			env.checkers[m.ID] = &blocking{started: make(chan struct{}, 1)}
			env.mu.Unlock()
			for i, ok := range tt.checks {
				c := store.Check{MonitorID: m.ID, At: now.Add(time.Duration(i-10) * time.Minute), OK: ok}
				if !ok {
					c.Error = "HTTP 500"
				}
				if err := env.store.InsertCheck(ctx, c); err != nil {
					t.Fatal(err)
				}
			}
			if tt.incident {
				if _, err := env.store.OpenIncident(ctx, m.ID, now, "HTTP 500"); err != nil {
					t.Fatal(err)
				}
			}
			if err := env.engine.Start(ctx); err != nil {
				t.Fatal(err)
			}
			st, ok := env.engine.Status(m.ID)
			if !ok || st.State != tt.wantState || st.Fails != tt.wantFails {
				t.Fatalf("status = %+v (ok=%v), want %s fails=%d", st, ok, tt.wantState, tt.wantFails)
			}
		})
	}
}

func TestReady(t *testing.T) {
	env := newEnv(t)
	if err := env.engine.Ready(); err == nil || err.Error() != "not started" {
		t.Fatalf("Ready before start = %v", err)
	}
	if err := env.engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := env.engine.Ready(); err != nil {
		t.Fatalf("Ready after start = %v", err)
	}
	env.engine.Stop()
	if err := env.engine.Ready(); err == nil || err.Error() != "stopped" {
		t.Fatalf("Ready after stop = %v", err)
	}
}

func TestHub(t *testing.T) {
	h := NewHub()
	a, stopA := h.Subscribe(1)
	b, stopB := h.Subscribe(1)
	defer stopB()
	h.Publish(Event{MonitorID: 1})
	h.Publish(Event{MonitorID: 2}) // dropped: buffers are full
	if ev := <-a; ev.MonitorID != 1 {
		t.Fatalf("a got %+v", ev)
	}
	if ev := <-b; ev.MonitorID != 1 {
		t.Fatalf("b got %+v", ev)
	}
	stopA()
	stopA() // idempotent
	h.Publish(Event{MonitorID: 3})
	if len(a) != 0 {
		t.Fatal("unsubscribed channel received an event")
	}
	if ev := <-b; ev.MonitorID != 3 {
		t.Fatalf("b got %+v", ev)
	}
}

func TestCheckNow(t *testing.T) {
	env := newEnv(t)
	if err := env.engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	events, stop := env.engine.Hub().Subscribe(64)
	defer stop()

	// A long interval so the scheduler does not run a check first.
	m := &store.Monitor{Name: "m", Type: store.TypeHTTP, Target: "x", IntervalS: 900}
	if err := env.store.CreateMonitor(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	env.mu.Lock()
	env.checkers[m.ID] = &scripted{results: []check.Result{{Error: "HTTP 503"}}}
	env.mu.Unlock()
	if err := env.engine.Reload(context.Background(), m.ID); err != nil {
		t.Fatal(err)
	}

	res, err := env.engine.CheckNow(context.Background(), m.ID)
	if err != nil || res.OK || res.Error != "HTTP 503" {
		t.Fatalf("CheckNow = %+v, %v", res, err)
	}
	ev := waitFor(t, events, 2*time.Second, func(ev Event) bool { return ev.MonitorID == m.ID && ev.Result.Error == "HTTP 503" })
	if ev.State != Pending {
		t.Fatalf("state after one failure = %s, want PENDING", ev.State)
	}
	checks, err := env.store.RecentChecks(context.Background(), m.ID, 5)
	if err != nil || len(checks) != 1 || checks[0].Error != "HTTP 503" {
		t.Fatalf("stored checks = %+v, %v", checks, err)
	}

	p := env.addMonitor(t, store.TypePush, nil)
	if _, err := env.engine.CheckNow(context.Background(), p.ID); !errors.Is(err, ErrNoChecker) {
		t.Fatalf("push CheckNow err = %v, want ErrNoChecker", err)
	}
	if _, err := env.engine.CheckNow(context.Background(), 9999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing CheckNow err = %v, want ErrNotFound", err)
	}
}

func TestCertificateWarning(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()
	soon := time.Now().Add(10 * 24 * time.Hour).Truncate(time.Second)
	checker := &scripted{results: []check.Result{{OK: true, Latency: time.Millisecond, CertExpiry: soon}}}
	m := env.addMonitor(t, store.TypeHTTP, checker)
	events, stop := env.engine.Hub().Subscribe(64)
	defer stop()
	if err := env.engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// Wait for three checks: the warning must go out once.
	for i := 0; i < 3; i++ {
		waitFor(t, events, 3*time.Second, func(ev Event) bool { return ev.MonitorID == m.ID && ev.Result.OK })
	}
	env.engine.Stop()
	waitAlerts(t, env.notifier, AlertCert, 1)
	saved, _ := env.store.Monitor(ctx, m.ID)
	if !saved.CertWarnedAt.Equal(soon) {
		t.Fatalf("CertWarnedAt = %v, want %v", saved.CertWarnedAt, soon)
	}

	// A restart with the same certificate sends nothing.
	env2 := &testEnv{store: env.store, notifier: &recordingNotifier{}, checkers: env.checkers}
	env2.engine = New(env.store, Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Notifier: env2.notifier})
	env2.engine.interval = env.engine.interval
	env2.engine.newChecker = env.engine.newChecker
	events2, stop2 := env2.engine.Hub().Subscribe(64)
	defer stop2()
	if err := env2.engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, events2, 3*time.Second, func(ev Event) bool { return ev.MonitorID == m.ID && ev.Result.OK })
	if got := count(env2.notifier.alerts(), AlertCert); got != 0 {
		t.Fatalf("cert alerts after restart = %d, want 0", got)
	}

	// A renewed certificate far in the future sends nothing. When it is
	// within 14 days, a new warning goes out.
	renewed := time.Now().Add(80 * 24 * time.Hour).Truncate(time.Second)
	checker.mu.Lock()
	checker.results = []check.Result{{OK: true, Latency: time.Millisecond, CertExpiry: renewed}}
	checker.i = 0
	checker.mu.Unlock()
	waitFor(t, events2, 3*time.Second, func(ev Event) bool { return ev.MonitorID == m.ID && ev.Result.CertExpiry.Equal(renewed) })
	if got := count(env2.notifier.alerts(), AlertCert); got != 0 {
		t.Fatalf("cert alerts after renewal = %d, want 0", got)
	}
	nearly := time.Now().Add(5 * 24 * time.Hour).Truncate(time.Second)
	checker.mu.Lock()
	checker.results = []check.Result{{OK: true, Latency: time.Millisecond, CertExpiry: nearly}}
	checker.i = 0
	checker.mu.Unlock()
	waitFor(t, events2, 3*time.Second, func(ev Event) bool { return ev.MonitorID == m.ID && ev.Result.CertExpiry.Equal(nearly) })
	env2.engine.Stop()
	waitAlerts(t, env2.notifier, AlertCert, 1)
}

// waitAlerts polls until the notifier has want alerts of a kind. Notify
// runs in its own goroutine, so a count right after an event can be early.
func waitAlerts(t *testing.T, n *recordingNotifier, kind Alert, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if got := count(n.alerts(), kind); got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("alerts of kind %v = %d, want %d", kind, count(n.alerts(), kind), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func count(alerts []Alert, kind Alert) int {
	var n int
	for _, a := range alerts {
		if a == kind {
			n++
		}
	}
	return n
}

// TestRetryAfterFailure covers SPEC.md section 4.4: after a failed check the
// next check runs after retryDelay, not the full interval. Two failures in a
// row still mean DOWN.
func TestRetryAfterFailure(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()
	m := env.addMonitor(t, store.TypeHTTP, &scripted{results: []check.Result{
		{Error: "HTTP 503", StatusCode: 503},
	}})
	// The interval is far longer than the retry delay.
	if err := env.store.UpdateMonitor(ctx, store.Monitor{ID: m.ID, Name: m.Name, Type: m.Type, Target: m.Target, IntervalS: 300}); err != nil {
		t.Fatal(err)
	}
	events, stop := env.engine.Hub().Subscribe(64)
	defer stop()
	if err := env.engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	first := waitFor(t, events, 3*time.Second, func(ev Event) bool { return ev.MonitorID == m.ID })
	if first.State != Pending || first.Alert != AlertNone {
		t.Fatalf("first failure event = %+v, want PENDING without alert", first)
	}
	firstAt := time.Now()
	down := waitFor(t, events, 3*time.Second, func(ev Event) bool { return ev.State == Down })
	if down.Alert != AlertDown {
		t.Fatalf("DOWN event = %+v", down)
	}
	retry, interval := env.engine.retryDelay, 300*testScale
	if got := time.Since(firstAt); got < retry/2 || got >= interval {
		t.Fatalf("second check after %v, want about %v (interval %v)", got, retry, interval)
	}
}

// TestPushGrace covers SPEC.md section 5.5: the grace is the interval plus
// 25%, with at least pushMinExtra extra.
func TestPushGrace(t *testing.T) {
	e := New(nil, Options{})
	tests := []struct {
		interval time.Duration
		want     time.Duration
	}{
		{30 * time.Second, 60 * time.Second},
		{time.Minute, 90 * time.Second},
		{2 * time.Minute, 150 * time.Second},
		{5 * time.Minute, 375 * time.Second},
		{24 * time.Hour, 30 * time.Hour},
	}
	for _, tt := range tests {
		if got := e.pushGrace(tt.interval); got != tt.want {
			t.Errorf("pushGrace(%v) = %v, want %v", tt.interval, got, tt.want)
		}
	}
}

// TestPushGraceTiming checks that a push monitor fails only after the grace
// period, and that the deadline counts from the last push.
func TestPushGraceTiming(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()
	m := env.addMonitor(t, store.TypePush, nil)
	if err := env.store.UpdateMonitor(ctx, store.Monitor{ID: m.ID, Name: m.Name, Type: m.Type, Target: m.Target, IntervalS: 50}); err != nil {
		t.Fatal(err)
	}
	events, stop := env.engine.Hub().Subscribe(64)
	defer stop()
	if err := env.engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// The engine stamps the push time inside Push, so take the reference
	// time before the call. Otherwise the miss can land a few ms early.
	pushed := time.Now()
	if err := env.engine.Push(ctx, m.PushToken); err != nil {
		t.Fatal(err)
	}
	waitFor(t, events, 2*time.Second, func(ev Event) bool { return ev.State == Up })
	grace := env.engine.pushGrace(50 * testScale) // 1s + 600ms
	fail := waitFor(t, events, 3*time.Second, func(ev Event) bool { return !ev.Result.OK })
	since := time.Since(pushed)
	if fail.Result.Error != "no push received" || since < grace || since > grace+500*time.Millisecond {
		t.Fatalf("first miss after %v (grace %v): %+v", since, grace, fail)
	}
}

// TestFirstDelay covers SPEC.md section 6.3: the first check counts from
// the last check, and a new monitor gets a small random offset.
func TestFirstDelay(t *testing.T) {
	e := New(nil, Options{})
	now := time.Now()
	const tol = 2 * time.Second
	tests := []struct {
		name     string
		interval time.Duration
		last     time.Time
		ok       bool
		wantMin  time.Duration
		wantMax  time.Duration
	}{
		{"no check, long interval: offset up to 60s", time.Hour, time.Time{}, false, 0, 60 * time.Second},
		{"no check, short interval: offset up to the interval", 30 * time.Second, time.Time{}, false, 0, 30 * time.Second},
		{"ok check 10s ago, 1m interval", time.Minute, now.Add(-10 * time.Second), true, 50*time.Second - tol, 50 * time.Second},
		{"ok check 2m ago, 1m interval: overdue", time.Minute, now.Add(-2 * time.Minute), true, 0, 0},
		{"failed check 10s ago: retry at 30s", time.Hour, now.Add(-10 * time.Second), false, 20*time.Second - tol, 20 * time.Second},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := store.Monitor{ID: int64(i + 1), Type: store.TypeHTTP}
			e.status[m.ID] = &Status{LastAt: tt.last, Last: check.Result{OK: tt.ok}}
			got := e.firstDelay(m, tt.interval)
			if got < tt.wantMin || got > tt.wantMax {
				t.Fatalf("firstDelay = %v, want %v to %v", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

// TestScheduleSurvivesRestart starts the engine with checks already in the
// database and expects the next check at the time the schedule says, not
// at start. The push monitor keeps its last push time.
func TestScheduleSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	t.Run("http waits for the rest of the interval", func(t *testing.T) {
		env := newEnv(t)
		m := &store.Monitor{Name: "m", Type: store.TypeHTTP, Target: "x", IntervalS: 100} // 2s
		if err := env.store.CreateMonitor(ctx, m); err != nil {
			t.Fatal(err)
		}
		// Check times are stored in whole seconds. The last check was 0 to
		// 1 second ago, so the next one is due in 1 to 2 seconds.
		last := time.Now().Truncate(time.Second)
		if err := env.store.InsertCheck(ctx, store.Check{MonitorID: m.ID, At: last, OK: true}); err != nil {
			t.Fatal(err)
		}
		events, stop := env.engine.Hub().Subscribe(64)
		defer stop()
		started := time.Now()
		if err := env.engine.Start(ctx); err != nil {
			t.Fatal(err)
		}
		select {
		case ev := <-events:
			t.Fatalf("check ran %v after start: %+v", time.Since(started), ev)
		case <-time.After(800 * time.Millisecond):
		}
		ev := waitFor(t, events, 2*time.Second, func(ev Event) bool { return ev.MonitorID == m.ID })
		want := last.Add(2 * time.Second)
		if ev.At.Before(want) || ev.At.After(want.Add(500*time.Millisecond)) {
			t.Fatalf("check ran at %v, want about %v", ev.At, want)
		}
	})
	t.Run("http overdue runs at once", func(t *testing.T) {
		env := newEnv(t)
		m := &store.Monitor{Name: "m", Type: store.TypeHTTP, Target: "x", IntervalS: 1000} // 20s
		if err := env.store.CreateMonitor(ctx, m); err != nil {
			t.Fatal(err)
		}
		if err := env.store.InsertCheck(ctx, store.Check{MonitorID: m.ID, At: time.Now().Add(-time.Hour), OK: true}); err != nil {
			t.Fatal(err)
		}
		events, stop := env.engine.Hub().Subscribe(64)
		defer stop()
		if err := env.engine.Start(ctx); err != nil {
			t.Fatal(err)
		}
		waitFor(t, events, 500*time.Millisecond, func(ev Event) bool { return ev.MonitorID == m.ID })
	})
	t.Run("push keeps its last push time", func(t *testing.T) {
		env := newEnv(t)
		m := &store.Monitor{Name: "m", Type: store.TypePush, Target: "x", IntervalS: 50} // grace 1.6s
		if err := env.store.CreateMonitor(ctx, m); err != nil {
			t.Fatal(err)
		}
		// The last push was 1s ago, so the grace ends 600ms after start.
		last := time.Now().Add(-time.Second).Truncate(time.Second)
		if err := env.store.InsertCheck(ctx, store.Check{MonitorID: m.ID, At: last, OK: true}); err != nil {
			t.Fatal(err)
		}
		events, stop := env.engine.Hub().Subscribe(64)
		defer stop()
		if err := env.engine.Start(ctx); err != nil {
			t.Fatal(err)
		}
		if st, _ := env.engine.Status(m.ID); !st.LastPush.Equal(last) {
			t.Fatalf("LastPush = %v, want %v", st.LastPush, last)
		}
		fail := waitFor(t, events, 3*time.Second, func(ev Event) bool { return ev.MonitorID == m.ID })
		want := last.Add(env.engine.pushGrace(50 * testScale))
		if fail.Result.OK || fail.At.Before(want) || fail.At.After(want.Add(500*time.Millisecond)) {
			t.Fatalf("miss at %v, want about %v: %+v", fail.At, want, fail)
		}
	})
}
