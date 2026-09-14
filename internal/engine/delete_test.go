package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/check"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// gate is a checker that reports its start and fails when the test opens
// the gate.
type gate struct {
	started chan struct{}
	open    chan struct{}
}

func (g *gate) Check(ctx context.Context) check.Result {
	select {
	case g.started <- struct{}{}:
	default:
	}
	select {
	case <-g.open:
	case <-ctx.Done():
	}
	return check.Result{Error: "HTTP 503"}
}

// TestResultAfterDeleteIsDropped starts a check, deletes the monitor while
// the check runs, and then lets the check finish. The writer must drop the
// result: no check row, no incident and no status for the deleted monitor.
func TestResultAfterDeleteIsDropped(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()
	// Both monitors had a successful check just now, so their runners wait a
	// full interval and every result below comes from CheckNow.
	idle := func(c check.Checker) store.Monitor {
		t.Helper()
		m := &store.Monitor{Name: "m", Type: store.TypeHTTP, Target: "x", IntervalS: 100000}
		if err := env.store.CreateMonitor(ctx, m); err != nil {
			t.Fatal(err)
		}
		if c != nil {
			env.mu.Lock()
			env.checkers[m.ID] = c
			env.mu.Unlock()
		}
		// A failure after this success does not open an incident. The failed
		// check row is what the delete must not leave behind.
		if err := env.store.InsertCheck(ctx, store.Check{MonitorID: m.ID, At: time.Now(), OK: true}); err != nil {
			t.Fatal(err)
		}
		return *m
	}
	g := &gate{started: make(chan struct{}, 1), open: make(chan struct{})}
	gone, marker := idle(g), idle(nil)
	events, stop := env.engine.Hub().Subscribe(64)
	defer stop()
	if err := env.engine.Start(ctx); err != nil {
		t.Fatal(err)
	}

	checked := make(chan error, 1)
	go func() {
		_, err := env.engine.CheckNow(ctx, gone.ID)
		checked <- err
	}()
	select {
	case <-g.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the check did not start")
	}
	if err := env.engine.DeleteMonitor(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	close(g.open)
	// CheckNow returns after it queued the result.
	if err := <-checked; err != nil {
		t.Fatalf("CheckNow: %v", err)
	}
	// The writer takes results in order, so once the marker result is out,
	// the result of the deleted monitor has been handled.
	if _, err := env.engine.CheckNow(ctx, marker.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, events, 5*time.Second, func(ev Event) bool { return ev.MonitorID == marker.ID })

	if checks, err := env.store.RecentChecks(ctx, gone.ID, 10); err != nil || len(checks) != 0 {
		t.Fatalf("checks of the deleted monitor = %+v, %v; want none", checks, err)
	}
	if _, err := env.store.CurrentIncident(ctx, gone.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("incident of the deleted monitor: err = %v, want ErrNotFound", err)
	}
	if _, ok := env.engine.Status(gone.ID); ok {
		t.Fatal("the engine keeps a status for the deleted monitor")
	}
	if err := env.engine.Reload(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
}
