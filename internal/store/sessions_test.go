package store

import (
	"context"
	"testing"
	"time"
)

// TestRunRetentionDeletesExpiredSessions starts the hourly job with an
// expired and a valid session. The first run must delete the expired one and
// keep the other.
func TestRunRetentionDeletesExpiredSessions(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.CreateSession(ctx, "expired", now.Add(-31*24*time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, "valid", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	count := func(hash string) int {
		t.Helper()
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE token_hash = ?`, hash).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	jobCtx, cancel := context.WithCancel(ctx)
	done := s.RunRetention(jobCtx, discardLogger())
	defer func() {
		cancel()
		<-done
	}()
	deadline := time.Now().Add(5 * time.Second)
	for count("expired") != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the expired session is still there after the first run")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if count("valid") != 1 {
		t.Fatal("the job deleted the valid session")
	}
}
