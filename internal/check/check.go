// Package check runs one check against one target.
package check

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"time"
)

// Timeout is the fixed time limit for one check.
const Timeout = 10 * time.Second

// Result is the outcome of one check.
type Result struct {
	OK         bool
	Latency    time.Duration
	StatusCode int       // HTTP only
	Error      string    // short, human-readable: "HTTP 503", "connection refused", "keyword not found"
	CertExpiry time.Time // HTTPS only
}

// Checker runs one check.
type Checker interface {
	Check(ctx context.Context) Result
}

// Spec describes what to check. It mirrors the monitor fields.
type Spec struct {
	Type       string // http, tcp, ping, dns
	Target     string // URL, host:port, host, or hostname
	Keyword    string // http only
	ExpectedIP string // dns only
}

// New returns the checker for a spec. Push monitors have no checker; the
// engine tracks them by the time of the last push.
func New(s Spec) (Checker, error) {
	switch s.Type {
	case "http":
		return &HTTP{URL: s.Target, Keyword: s.Keyword}, nil
	case "tcp":
		return &TCP{Address: s.Target}, nil
	case "ping":
		return &Ping{Host: s.Target}, nil
	case "dns":
		return &DNS{Hostname: s.Target, ExpectedIP: s.ExpectedIP}, nil
	default:
		return nil, fmt.Errorf("unknown monitor type %q", s.Type)
	}
}

func fail(err error) Result {
	return Result{Error: Describe(err)}
}

// Describe turns a Go error into a short message for non-experts.
func Describe(err error) string {
	if err == nil {
		return ""
	}
	var certErr x509.CertificateInvalidError
	var hostErr x509.HostnameError
	var unknownAuth x509.UnknownAuthorityError
	var dnsErr *net.DNSError
	var opErr *net.OpError
	switch {
	case errors.As(err, &dnsErr):
		if dnsErr.IsNotFound {
			return "no such host"
		}
		if dnsErr.IsTimeout {
			return "DNS timeout"
		}
		return "DNS lookup failed"
	case errors.Is(err, context.DeadlineExceeded), os.IsTimeout(err):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.As(err, &certErr):
		if certErr.Reason == x509.Expired {
			return "certificate expired"
		}
		return "invalid certificate"
	case errors.As(err, &hostErr):
		return "certificate name mismatch"
	case errors.As(err, &unknownAuth):
		return "untrusted certificate"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return "host unreachable"
	case errors.As(err, &opErr) && opErr.Timeout():
		return "timeout"
	}
	msg := err.Error()
	if strings.Contains(msg, "tls:") || strings.Contains(msg, "x509:") {
		return "invalid certificate"
	}
	if i := strings.LastIndex(msg, ": "); i >= 0 {
		msg = msg[i+2:]
	}
	if len(msg) > 60 {
		msg = msg[:60]
	}
	return msg
}
