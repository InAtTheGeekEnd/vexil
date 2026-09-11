package check

import (
	"context"
	"net"
	"time"
)

// TCP checks that a TCP connection to Address ("host:port") opens.
type TCP struct {
	Address string
}

func (t *TCP) Check(ctx context.Context) Result {
	if _, _, err := net.SplitHostPort(t.Address); err != nil {
		return Result{Error: "invalid host:port"}
	}
	var d net.Dialer
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", t.Address)
	if err != nil {
		return fail(err)
	}
	latency := time.Since(start)
	conn.Close()
	return Result{OK: true, Latency: latency}
}
