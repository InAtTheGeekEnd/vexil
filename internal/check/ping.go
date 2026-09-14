package check

import (
	"context"
	"strings"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

// pingMargin ends the ping run this long before the check deadline, so the
// run stops on its own and its replies are counted. A cancelled context
// would end it with an error first.
const pingMargin = 500 * time.Millisecond

// Ping sends 3 ICMP echo requests. It succeeds when at least one reply
// arrives. Latency is the average of the replies.
//
// It uses unprivileged UDP ping. On Linux this needs the sysctl
// net.ipv4.ping_group_range to include the process group.
type Ping struct {
	Host string

	// run sends the requests and returns the statistics, with an error when
	// the run did not end normally. Nil means a real ping. Tests replace it.
	run func(ctx context.Context, host string) (*probing.Statistics, error)
}

func (p *Ping) Check(ctx context.Context) Result {
	run := p.run
	if run == nil {
		run = runPing
	}
	stats, err := run(ctx, p.Host)
	// Replies count even when the run ended with an error, as it does when
	// the deadline comes before the run stops.
	if stats != nil && stats.PacketsRecv > 0 {
		return Result{OK: true, Latency: stats.AvgRtt}
	}
	if ctx.Err() != nil {
		return fail(ctx.Err())
	}
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "not permitted") || strings.Contains(msg, "permission denied") {
			return Result{Error: "ping not permitted on this server"}
		}
		return fail(err)
	}
	return Result{Error: "no reply"}
}

// runPing sends 3 echo requests and waits for the replies until the run
// ends: after 3 replies, or pingMargin before the deadline of ctx.
func runPing(ctx context.Context, host string) (*probing.Statistics, error) {
	pinger, err := probing.NewPinger(host)
	if err != nil {
		return nil, err
	}
	pinger.SetPrivileged(false)
	pinger.Count = 3
	pinger.Interval = 200 * time.Millisecond
	pinger.Timeout = Timeout - pingMargin
	if dl, ok := ctx.Deadline(); ok {
		pinger.Timeout = time.Until(dl) - pingMargin
	}
	if pinger.Timeout <= 0 {
		return nil, context.DeadlineExceeded
	}
	err = pinger.RunWithContext(ctx)
	return pinger.Statistics(), err
}
