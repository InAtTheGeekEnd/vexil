package check

import (
	"context"
	"strings"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

// Ping sends 3 ICMP echo requests. It succeeds when at least one reply
// arrives. Latency is the average of the replies.
//
// It uses unprivileged UDP ping. On Linux this needs the sysctl
// net.ipv4.ping_group_range to include the process group.
type Ping struct {
	Host string
}

func (p *Ping) Check(ctx context.Context) Result {
	pinger, err := probing.NewPinger(p.Host)
	if err != nil {
		return fail(err)
	}
	pinger.SetPrivileged(false)
	pinger.Count = 3
	pinger.Interval = 200 * time.Millisecond
	pinger.Timeout = Timeout
	if dl, ok := ctx.Deadline(); ok {
		pinger.Timeout = time.Until(dl)
	}
	if err := pinger.RunWithContext(ctx); err != nil {
		if ctx.Err() != nil {
			return fail(ctx.Err())
		}
		msg := err.Error()
		if strings.Contains(msg, "not permitted") || strings.Contains(msg, "permission denied") {
			return Result{Error: "ping not permitted on this server"}
		}
		return fail(err)
	}
	stats := pinger.Statistics()
	if stats.PacketsRecv == 0 {
		return Result{Error: "no reply"}
	}
	return Result{OK: true, Latency: stats.AvgRtt}
}
