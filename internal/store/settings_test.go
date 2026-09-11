package store

import (
	"context"
	"testing"
	"time"
)

func TestSetPasswordHashKeepsOneSession(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	for _, h := range []string{"mine", "other"} {
		if err := s.CreateSession(ctx, h, now, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetPasswordHash(ctx, "hash", "mine"); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		hash string
		want bool
	}{{"mine", true}, {"other", false}}
	for _, tc := range tests {
		if ok, err := s.SessionValid(ctx, tc.hash, now); err != nil || ok != tc.want {
			t.Errorf("SessionValid(%q) = %v, %v; want %v", tc.hash, ok, err, tc.want)
		}
	}
}

func TestSettingsMany(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.SetSettings(ctx, map[string]string{SettingBrandName: "Acme", SettingBrandAccent: "#000000"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Settings(ctx, SettingBrandName, SettingBrandAccent, SettingBrandLogo)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[SettingBrandName] != "Acme" || got[SettingBrandAccent] != "#000000" {
		t.Fatalf("Settings = %v", got)
	}
	if err := s.DeleteSettings(ctx, SettingBrandName, SettingBrandLogo); err != nil {
		t.Fatal(err)
	}
	got, err = s.Settings(ctx, SettingBrandName, SettingBrandAccent)
	if err != nil || len(got) != 1 || got[SettingBrandAccent] != "#000000" {
		t.Fatalf("Settings after delete = %v, %v", got, err)
	}
	if empty, err := s.Settings(ctx); err != nil || len(empty) != 0 {
		t.Fatalf("Settings() = %v, %v", empty, err)
	}
}

func TestRecentIncidents(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	pub := &Monitor{Name: "Public", Type: TypeHTTP, Target: "https://a.example", IntervalS: 60, Public: true}
	priv := &Monitor{Name: "Private", Type: TypeHTTP, Target: "https://b.example", IntervalS: 60}
	for _, m := range []*Monitor{pub, priv} {
		if err := s.CreateMonitor(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	// Old, closed a month ago.
	if _, err := s.OpenIncident(ctx, pub.ID, now.AddDate(0, 0, -40), "old"); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseIncident(ctx, pub.ID, now.AddDate(0, 0, -30)); err != nil {
		t.Fatal(err)
	}
	// Recent, closed yesterday.
	if _, err := s.OpenIncident(ctx, pub.ID, now.AddDate(0, 0, -2), "HTTP 503"); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseIncident(ctx, pub.ID, now.AddDate(0, 0, -1)); err != nil {
		t.Fatal(err)
	}
	// Open on the private monitor.
	if _, err := s.OpenIncident(ctx, priv.ID, now.Add(-time.Hour), "timeout"); err != nil {
		t.Fatal(err)
	}
	since := now.AddDate(0, 0, -14)

	all, err := s.RecentIncidents(ctx, since, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].MonitorName != "Private" || !all[0].Open() || all[1].Reason != "HTTP 503" {
		t.Fatalf("all = %+v", all)
	}
	public, err := s.RecentIncidents(ctx, since, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(public) != 1 || public[0].MonitorName != "Public" || !public[0].EndedAt.Equal(now.AddDate(0, 0, -1)) {
		t.Fatalf("public = %+v", public)
	}
}
