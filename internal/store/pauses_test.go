package store

import (
	"context"
	"testing"
	"time"
)

// TestPauses pauses and resumes a monitor and reads the pauses back. A
// second pause of a paused monitor adds no row. A monitor created paused
// starts its pause at its creation.
func TestPauses(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	m := &Monitor{Name: "m", Type: TypeHTTP, Target: "https://x", IntervalS: 60}
	if err := s.CreateMonitor(ctx, m); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	steps := []struct {
		paused bool
		at     time.Time
	}{
		{true, now.Add(-3 * time.Hour)},
		{true, now.Add(-150 * time.Minute)},
		{false, now.Add(-2 * time.Hour)},
		{false, now.Add(-90 * time.Minute)},
		{true, now.Add(-time.Hour)},
	}
	for _, st := range steps {
		if err := s.SetPaused(ctx, m.ID, st.paused, st.at); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.MonitorPauses(ctx, m.ID, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	want := []Pause{
		{MonitorID: m.ID, StartedAt: now.Add(-3 * time.Hour), EndedAt: now.Add(-2 * time.Hour)},
		{MonitorID: m.ID, StartedAt: now.Add(-time.Hour)},
	}
	if len(got) != len(want) {
		t.Fatalf("pauses = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].MonitorID != want[i].MonitorID || !got[i].StartedAt.Equal(want[i].StartedAt) || !got[i].EndedAt.Equal(want[i].EndedAt) {
			t.Errorf("pause %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if got[1].Open() != true || got[0].Open() != false {
		t.Error("Open() is wrong")
	}
	// The window leaves out a pause that ended before it.
	recent, err := s.MonitorPauses(ctx, m.ID, now.Add(-90*time.Minute))
	if err != nil || len(recent) != 1 || !recent[0].Open() {
		t.Fatalf("recent pauses = %+v, %v; want the open one", recent, err)
	}
	if mm, err := s.Monitor(ctx, m.ID); err != nil || !mm.Paused {
		t.Fatalf("monitor paused = %v, %v; want true", mm.Paused, err)
	}

	born := &Monitor{Name: "p", Type: TypeTCP, Target: "x:1", IntervalS: 60, Paused: true, CreatedAt: now.Add(-time.Minute)}
	if err := s.CreateMonitor(ctx, born); err != nil {
		t.Fatal(err)
	}
	all, err := s.PausesSince(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(all[m.ID]) != 2 || len(all[born.ID]) != 1 || !all[born.ID][0].StartedAt.Equal(born.CreatedAt) {
		t.Fatalf("PausesSince = %+v", all)
	}
	if err := s.SetPaused(ctx, 999, true, now); err != ErrNotFound {
		t.Fatalf("SetPaused of a missing monitor = %v, want ErrNotFound", err)
	}
}
