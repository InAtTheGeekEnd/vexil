package engine

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/check"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// stuck is a checker that ignores its context and returns only when the test
// releases it, like a check blocked in a call that takes no context.
type stuck struct {
	started chan struct{}
	release chan struct{}
}

func (s *stuck) Check(context.Context) check.Result {
	s.started <- struct{}{}
	<-s.release
	return check.Result{OK: true}
}

// slowNotifier takes a moment before it records an alert, as a notifier that
// reads the store first does.
type slowNotifier struct{ recordingNotifier }

func (n *slowNotifier) Notify(ctx context.Context, ev Event) {
	time.Sleep(300 * time.Millisecond)
	n.recordingNotifier.Notify(ctx, ev)
}

// TestStopWaitsForAlerts turns a monitor DOWN and stops the engine at once.
// The engine hands the DOWN alert to the notifier in a goroutine. Stop must
// wait for that call: main closes the database right after Stop, so an alert
// that is still on its way is lost.
func TestStopWaitsForAlerts(t *testing.T) {
	env := newEnv(t)
	env.engine.stopWait = 5 * time.Second
	slow := &slowNotifier{}
	env.engine.notifier = slow
	m := env.addMonitor(t, store.TypeHTTP, &scripted{results: []check.Result{{Error: "HTTP 503"}}})
	events, stop := env.engine.Hub().Subscribe(64)
	defer stop()
	if err := env.engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, events, 5*time.Second, func(ev Event) bool { return ev.MonitorID == m.ID && ev.Alert == AlertDown })

	env.engine.Stop()
	if got := slow.alerts(); !slices.Contains(got, AlertDown) {
		t.Fatalf("alerts when Stop returned = %v, want the DOWN alert", got)
	}
}

// TestStopWaitsForRunningChecks stops the engine while a check runs. Stop
// must stop the schedule but let the running check end and store its
// result, as SPEC.md section 6.3 says: stop tickers, wait for running checks,
// flush the writer.
func TestStopWaitsForRunningChecks(t *testing.T) {
	env := newEnv(t)
	env.engine.stopWait = 5 * time.Second
	ctx := context.Background()
	g := &gate{started: make(chan struct{}, 1), open: make(chan struct{})}
	m := env.addMonitor(t, store.TypeHTTP, g)
	if err := env.engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-g.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the check did not start")
	}

	stopped := make(chan struct{})
	go func() {
		env.engine.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("Stop returned while a check was still running")
	case <-time.After(200 * time.Millisecond):
	}
	close(g.open)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after the check ended")
	}
	checks, err := env.store.RecentChecks(ctx, m.ID, 10)
	if err != nil || len(checks) != 1 || checks[0].Error != "HTTP 503" {
		t.Fatalf("stored checks = %+v, %v; want the result of the running check", checks, err)
	}
}

// TestStopDeadlineCoversEveryRunner starts two monitors whose checks ignore
// cancellation and do not end. Stop must return soon after its deadline, not
// wait without a limit for the second runner.
func TestStopDeadlineCoversEveryRunner(t *testing.T) {
	env := newEnv(t)
	env.engine.stopWait = 200 * time.Millisecond
	c := &stuck{started: make(chan struct{}, 2), release: make(chan struct{})}
	t.Cleanup(func() { close(c.release) })
	for i := 0; i < 2; i++ {
		env.addMonitor(t, store.TypeHTTP, c)
	}
	if err := env.engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-c.started:
		case <-time.After(5 * time.Second):
			t.Fatal("the checks did not start")
		}
	}

	stopped := make(chan struct{})
	go func() {
		env.engine.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop still waits 3s after its 200ms deadline")
	}
}
