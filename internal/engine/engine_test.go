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
	// Intervals are stored in seconds. Tests run them as milliseconds x 20.
	env.engine.interval = func(m store.Monitor) time.Duration {
		return time.Duration(m.IntervalS) * 20 * time.Millisecond
	}
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
	// No further pushes: the monitor fails after 2 x interval and goes DOWN.
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
