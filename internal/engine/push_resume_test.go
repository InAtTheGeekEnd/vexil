package engine

import (
	"context"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// TestPushResumeRestartsTheDeadline pushes, pauses the push monitor for
// longer than its grace period and resumes it. No push was due while it was
// paused, so the deadline counts from the resume: no miss may come before a
// full grace period after it.
func TestPushResumeRestartsTheDeadline(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()
	m := env.addMonitor(t, store.TypePush, nil)
	events, stop := env.engine.Hub().Subscribe(64)
	defer stop()
	if err := env.engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := env.engine.Push(ctx, m.PushToken); err != nil {
		t.Fatal(err)
	}
	waitFor(t, events, 2*time.Second, func(ev Event) bool { return ev.State == Up })

	setPaused := func(paused bool, want State) {
		t.Helper()
		if err := env.store.SetPaused(ctx, m.ID, paused, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := env.engine.Reload(ctx, m.ID); err != nil {
			t.Fatal(err)
		}
		waitFor(t, events, 2*time.Second, func(ev Event) bool { return ev.State == want })
	}
	setPaused(true, Paused)
	grace := env.engine.pushGrace(testScale) // 20ms + 600ms
	// The push is now older than the grace period.
	time.Sleep(grace + 200*time.Millisecond)
	resumed := time.Now()
	setPaused(false, Pending)

	miss := waitFor(t, events, 5*time.Second, func(ev Event) bool { return ev.Result.Error == "no push received" })
	if since := miss.At.Sub(resumed); since < grace {
		t.Fatalf("first miss %v after the resume, want at least the grace period %v", since, grace)
	}
}
