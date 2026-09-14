package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestSetFirstPasswordHash sets the first password twice. The second call
// must store nothing and report false. The first call ends every session.
func TestSetFirstPasswordHash(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.CreateSession(ctx, "old", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for i, want := range []bool{true, false} {
		set, err := s.SetFirstPasswordHash(ctx, fmt.Sprintf("hash%d", i))
		if err != nil || set != want {
			t.Fatalf("call %d: set = %v, err = %v; want %v", i+1, set, err, want)
		}
	}
	if h, err := s.PasswordHash(ctx); err != nil || h != "hash0" {
		t.Fatalf("hash = %q, %v; want the first one", h, err)
	}
	if ok, err := s.SessionValid(ctx, "old", now); err != nil || ok {
		t.Fatalf("old session valid = %v, %v; want false", ok, err)
	}
}
