package check

import (
	"context"
	"errors"
	"testing"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

// TestPingCountsReplies runs Check with a stubbed ping run that gets 0 to 3
// replies. One reply or more is a success with the average latency, also
// when the deadline ended the run with an error. "timeout" comes only with
// no reply.
func TestPingCountsReplies(t *testing.T) {
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	background := context.Background()
	tests := []struct {
		name        string
		ctx         context.Context
		replies     int
		avg         time.Duration
		runErr      error
		wantOK      bool
		wantLatency time.Duration
		wantErr     string
	}{
		{"3 replies", background, 3, 12 * time.Millisecond, nil, true, 12 * time.Millisecond, ""},
		{"2 replies", background, 2, 15 * time.Millisecond, nil, true, 15 * time.Millisecond, ""},
		{"2 replies, the deadline ended the run", expired, 2, 15 * time.Millisecond, context.DeadlineExceeded, true, 15 * time.Millisecond, ""},
		{"1 reply, the deadline ended the run", expired, 1, 20 * time.Millisecond, context.DeadlineExceeded, true, 20 * time.Millisecond, ""},
		{"0 replies, the deadline ended the run", expired, 0, 0, context.DeadlineExceeded, false, 0, "timeout"},
		{"0 replies, the run ended", background, 0, 0, nil, false, 0, "no reply"},
		{"0 replies, ping not permitted", background, 0, 0, errors.New("socket: operation not permitted"), false, 0, "ping not permitted on this server"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Ping{Host: "192.0.2.1", run: func(context.Context, string) (*probing.Statistics, error) {
				return &probing.Statistics{PacketsSent: 3, PacketsRecv: tt.replies, AvgRtt: tt.avg}, tt.runErr
			}}
			got := p.Check(tt.ctx)
			if got.OK != tt.wantOK || got.Latency != tt.wantLatency || got.Error != tt.wantErr {
				t.Fatalf("Check = %+v, want OK=%v latency=%v error=%q", got, tt.wantOK, tt.wantLatency, tt.wantErr)
			}
		})
	}
}

// TestPingRunEndsBeforeDeadline checks that a real run stops on its own
// before the check deadline, so its replies are read, and that a deadline
// too close for a run gives "timeout" without a panic.
func TestPingRunEndsBeforeDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), pingMargin/2)
	defer cancel()
	if got := (&Ping{Host: "127.0.0.1"}).Check(ctx); got.OK || got.Error != "timeout" {
		t.Fatalf("Check with a deadline inside the margin = %+v, want timeout", got)
	}

	// 192.0.2.1 is a documentation address: no reply comes back.
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	got := (&Ping{Host: "192.0.2.1"}).Check(ctx)
	if got.Error == "ping not permitted on this server" {
		t.Skip("unprivileged ping is not allowed here")
	}
	if got.OK || got.Error != "no reply" {
		t.Fatalf("Check with no reply = %+v, want no reply", got)
	}
	if ctx.Err() != nil {
		t.Fatalf("the run ended after the deadline, at %v", time.Since(start))
	}
}
