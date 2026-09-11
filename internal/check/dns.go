package check

import (
	"context"
	"net"
	"time"
)

// DNS checks that Hostname resolves to at least one A or AAAA record. If
// ExpectedIP is set, one record must equal it.
type DNS struct {
	Hostname   string
	ExpectedIP string
	// Resolver overrides the resolver. Nil uses the system resolver.
	Resolver *net.Resolver
}

func (d *DNS) Check(ctx context.Context) Result {
	r := d.Resolver
	if r == nil {
		r = net.DefaultResolver
	}
	start := time.Now()
	addrs, err := r.LookupIPAddr(ctx, d.Hostname)
	if err != nil {
		return fail(err)
	}
	latency := time.Since(start)
	if len(addrs) == 0 {
		return Result{Error: "no records"}
	}
	if d.ExpectedIP != "" {
		want := net.ParseIP(d.ExpectedIP)
		found := false
		for _, a := range addrs {
			if a.IP.Equal(want) {
				found = true
				break
			}
		}
		if !found {
			return Result{Error: "expected IP not found", Latency: latency}
		}
	}
	return Result{OK: true, Latency: latency}
}
