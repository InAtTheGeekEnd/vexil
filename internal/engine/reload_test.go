package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/check"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// inFlight is a checker that blocks until its context ends and counts the
// checks that run. Each live runner of the monitor holds one check, so the
// count is the number of runners.
type inFlight struct{ n atomic.Int32 }

func (c *inFlight) Check(ctx context.Context) check.Result {
	c.n.Add(1)
	defer c.n.Add(-1)
	<-ctx.Done()
	// A check can take a moment to stop. The reload that stops it waits,
	// which gives an overlapping reload time to start a runner of its own.
	time.Sleep(50 * time.Millisecond)
	return check.Result{Error: "cancelled"}
}

// TestConcurrentReloads runs many Reload calls for one monitor at the same
// time. One runner must remain. A second round with a delete in the middle
// must leave no runner and no status.
func TestConcurrentReloads(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()
	c := &inFlight{}
	m := env.addMonitor(t, store.TypeHTTP, c)
	if err := env.engine.Start(ctx); err != nil {
		t.Fatal(err)
	}

	reloadAll := func(n int, during func()) {
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := env.engine.Reload(ctx, m.ID); err != nil {
					t.Error(err)
				}
			}()
			if i == n/2 && during != nil {
				during()
			}
		}
		wg.Wait()
	}
	// settle waits for the running checks to reach want. A runner starts
	// its first check within the start offset, 20 ms here, so a leaked
	// runner shows within the extra wait.
	settle := func(want int32) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for c.n.Load() != want && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond)
		if got := c.n.Load(); got != want {
			t.Fatalf("running checks = %d, want %d", got, want)
		}
	}

	settle(1)
	reloadAll(20, nil)
	settle(1)

	reloadAll(20, func() {
		if err := env.store.DeleteMonitor(ctx, m.ID); err != nil {
			t.Error(err)
		}
	})
	if err := env.engine.Reload(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	settle(0)
	if _, ok := env.engine.Status(m.ID); ok {
		t.Fatal("the status survived the delete")
	}
}
