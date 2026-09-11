package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestChannels(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	c := &Channel{Type: ChannelNtfy, Name: "Phone", Config: map[string]string{"url": "https://ntfy.sh/x", "token": "tk_1"}, Enabled: true}
	if err := s.CreateChannel(ctx, c); err != nil {
		t.Fatal(err)
	}
	off := &Channel{Type: ChannelWebhook, Name: "Hook", Config: map[string]string{"url": "https://example.com/h"}}
	if err := s.CreateChannel(ctx, off); err != nil {
		t.Fatal(err)
	}

	got, err := s.Channel(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Phone" || got.Config["token"] != "tk_1" || !got.Enabled || got.LastError != "" {
		t.Fatalf("channel = %+v", got)
	}

	enabled, err := s.EnabledChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 1 || enabled[0].ID != c.ID {
		t.Fatalf("enabled = %+v, want only %d", enabled, c.ID)
	}

	c.Name, c.Config["token"] = "Phone 2", "tk_2"
	if err := s.UpdateChannel(ctx, *c); err != nil {
		t.Fatal(err)
	}
	if err := s.SetChannelEnabled(ctx, c.ID, false); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1_700_000_000, 0)
	if err := s.SetChannelError(ctx, c.ID, "HTTP 403", at); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Channel(ctx, c.ID)
	if got.Name != "Phone 2" || got.Config["token"] != "tk_2" || got.Enabled || got.LastError != "HTTP 403" || !got.LastErrorAt.Equal(at) {
		t.Fatalf("updated channel = %+v", got)
	}
	if err := s.SetChannelError(ctx, c.ID, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Channel(ctx, c.ID); got.LastError != "" || !got.LastErrorAt.IsZero() {
		t.Fatalf("error not cleared: %+v", got)
	}

	if err := s.DeleteChannel(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Channel(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteChannel(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}
}

func TestSetCertWarned(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	m := &Monitor{Name: "m", Type: TypeHTTP, Target: "https://x"}
	if err := s.CreateMonitor(ctx, m); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Monitor(ctx, m.ID); !got.CertWarnedAt.IsZero() {
		t.Fatalf("new monitor has CertWarnedAt %v", got.CertWarnedAt)
	}
	expiry := time.Unix(1_800_000_000, 0)
	if err := s.SetCertWarned(ctx, m.ID, expiry); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Monitor(ctx, m.ID); !got.CertWarnedAt.Equal(expiry) {
		t.Fatalf("CertWarnedAt = %v, want %v", got.CertWarnedAt, expiry)
	}
}
