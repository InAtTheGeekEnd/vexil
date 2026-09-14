package engine

import (
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/check"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// TestOverdueChecksAreSpread gives many monitors a last check two hours ago,
// as after a long downtime. Their first checks must not all come at once:
// each gets a random offset within the limit of SPEC.md section 6.3.
func TestOverdueChecksAreSpread(t *testing.T) {
	e := New(nil, Options{})
	last := time.Now().Add(-2 * time.Hour)
	seen := map[time.Duration]bool{}
	for id := int64(1); id <= 50; id++ {
		e.status[id] = &Status{LastAt: last, Last: check.Result{OK: true}}
		d := e.firstDelay(store.Monitor{ID: id, Type: store.TypeHTTP}, time.Hour)
		if d < 0 || d >= defaultMaxOffset {
			t.Fatalf("monitor %d: first delay %v, want 0 to %v", id, d, defaultMaxOffset)
		}
		seen[d] = true
	}
	if len(seen) < 25 {
		t.Fatalf("50 overdue monitors got only %d different first delays", len(seen))
	}
}
