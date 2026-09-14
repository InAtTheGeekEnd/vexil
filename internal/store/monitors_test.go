package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMonitorCRUD(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	m := &Monitor{Name: "Site", Type: TypeHTTP, Target: "https://example.com", Keyword: "hello"}
	if err := s.CreateMonitor(ctx, m); err != nil {
		t.Fatal(err)
	}
	if m.ID == 0 || m.IntervalS != 60 || m.CreatedAt.IsZero() {
		t.Fatalf("defaults not filled: %+v", m)
	}
	p := &Monitor{Name: "Cron", Type: TypePush, IntervalS: 300}
	if err := s.CreateMonitor(ctx, p); err != nil {
		t.Fatal(err)
	}
	if len(p.PushToken) < 30 {
		t.Fatalf("push token too short: %q", p.PushToken)
	}

	got, err := s.Monitor(ctx, m.ID)
	if err != nil || got.Name != "Site" || got.Keyword != "hello" || got.ExpectedIP != "" {
		t.Fatalf("Monitor = %+v, %v", got, err)
	}
	if byTok, err := s.MonitorByPushToken(ctx, p.PushToken); err != nil || byTok.ID != p.ID {
		t.Fatalf("MonitorByPushToken = %+v, %v", byTok, err)
	}
	if _, err := s.MonitorByPushToken(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token err = %v", err)
	}

	all, err := s.Monitors(ctx)
	if err != nil || len(all) != 2 || all[0].ID != m.ID || all[1].Position != 2 {
		t.Fatalf("Monitors = %+v, %v", all, err)
	}

	got.Name = "Site 2"
	got.Keyword = ""
	got.Paused = true
	if err := s.UpdateMonitor(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Monitor(ctx, m.ID)
	if got.Name != "Site 2" || got.Keyword != "" || !got.Paused {
		t.Fatalf("after update: %+v", got)
	}
	if err := s.SetPaused(ctx, m.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Monitor(ctx, m.ID); got.Paused {
		t.Fatal("still paused")
	}

	if err := s.InsertCheck(ctx, Check{MonitorID: m.ID, At: time.Now(), OK: true, LatencyMS: 5}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMonitor(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Monitor(ctx, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete err = %v", err)
	}
	if cs, _ := s.RecentChecks(ctx, m.ID, 10); len(cs) != 0 {
		t.Fatal("checks survived delete")
	}
	if err := s.DeleteMonitor(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing err = %v", err)
	}
	if err := s.UpdateMonitor(ctx, Monitor{ID: 999}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing err = %v", err)
	}
}

// createMonitors adds n monitors to an empty store. They get the ids 1 to n,
// which the tests below use: the foreign keys refuse a row of a monitor that
// does not exist.
func createMonitors(t *testing.T, s *Store, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		m := &Monitor{Name: "m", Type: TypeHTTP, Target: "https://example.com", IntervalS: 60}
		if err := s.CreateMonitor(context.Background(), m); err != nil {
			t.Fatal(err)
		}
		if m.ID != int64(i) {
			t.Fatalf("monitor id = %d, want %d", m.ID, i)
		}
	}
}

func TestIncidentStartedAt(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	createMonitors(t, s, 2)
	if _, err := s.OpenIncident(ctx, 1, base, "timeout"); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseIncident(ctx, 1, base.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenIncident(ctx, 1, base.Add(5*time.Minute), "HTTP 503"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		monitorID int64
		started   time.Time
		wantOpen  bool
		wantErr   error
	}{
		{"closed older incident", 1, base, false, nil},
		{"open newer incident", 1, base.Add(5 * time.Minute), true, nil},
		{"compared to the second", 1, base.Add(500 * time.Millisecond), false, nil},
		{"no incident at that time", 1, base.Add(time.Minute), false, ErrNotFound},
		{"other monitor", 2, base, false, ErrNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inc, err := s.IncidentStartedAt(ctx, tc.monitorID, tc.started)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err == nil && (inc.Open() != tc.wantOpen || inc.StartedAt.Unix() != tc.started.Unix()) {
				t.Fatalf("incident = %+v", inc)
			}
		})
	}
}

func TestChecksAndIncidents(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	createMonitors(t, s, 2)

	checks := []Check{
		{MonitorID: 1, At: base, OK: true, LatencyMS: 120, StatusCode: 200},
		{MonitorID: 1, At: base.Add(time.Minute), OK: false, Error: "HTTP 503", StatusCode: 503},
		{MonitorID: 1, At: base.Add(2 * time.Minute), OK: false, Error: "timeout"},
		{MonitorID: 2, At: base.Add(2 * time.Minute), OK: true, LatencyMS: 3},
	}
	for _, c := range checks {
		if err := s.InsertCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.RecentChecks(ctx, 1, 2)
	if err != nil || len(got) != 2 {
		t.Fatalf("RecentChecks = %+v, %v", got, err)
	}
	if got[0].Error != "timeout" || got[0].LatencyMS != 0 || got[1].StatusCode != 503 {
		t.Fatalf("order or fields wrong: %+v", got)
	}

	if _, err := s.CurrentIncident(ctx, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no incident yet, err = %v", err)
	}
	id, err := s.OpenIncident(ctx, 1, base.Add(2*time.Minute), "timeout")
	if err != nil || id == 0 {
		t.Fatal(err)
	}
	cur, err := s.CurrentIncident(ctx, 1)
	if err != nil || !cur.Open() || cur.Reason != "timeout" {
		t.Fatalf("CurrentIncident = %+v, %v", cur, err)
	}
	if err := s.CloseIncident(ctx, 1, base.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CurrentIncident(ctx, 1); !errors.Is(err, ErrNotFound) {
		t.Fatal("incident still open")
	}
	list, err := s.Incidents(ctx, 1, 10)
	if err != nil || len(list) != 1 || list[0].Open() || !list[0].EndedAt.Equal(base.Add(5*time.Minute)) {
		t.Fatalf("Incidents = %+v, %v", list, err)
	}
}
