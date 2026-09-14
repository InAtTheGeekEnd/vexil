package engine

import (
	"context"
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
