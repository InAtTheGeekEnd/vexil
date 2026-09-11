package check

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.Write([]byte("hello world"))
		case "/503":
			w.WriteHeader(503)
		case "/redirect":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/slow":
			time.Sleep(300 * time.Millisecond)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tests := []struct {
		name    string
		url     string
		keyword string
		timeout time.Duration
		wantOK  bool
		wantErr string
		code    int
	}{
		{name: "200", url: srv.URL + "/ok", wantOK: true, code: 200},
		{name: "keyword found", url: srv.URL + "/ok", keyword: "world", wantOK: true, code: 200},
		{name: "keyword case sensitive", url: srv.URL + "/ok", keyword: "World", wantErr: "keyword not found", code: 200},
		{name: "503", url: srv.URL + "/503", wantErr: "HTTP 503", code: 503},
		{name: "404", url: srv.URL + "/missing", wantErr: "HTTP 404", code: 404},
		{name: "redirect followed", url: srv.URL + "/redirect", wantOK: true, code: 200},
		{name: "redirect loop", url: srv.URL + "/loop", wantErr: "too many redirects"},
		{name: "timeout", url: srv.URL + "/slow", timeout: 50 * time.Millisecond, wantErr: "timeout"},
		{name: "connection refused", url: "http://127.0.0.1:1", wantErr: "connection refused"},
		{name: "no such host", url: "http://no-such-host.invalid/", wantErr: "no such host"},
		{name: "invalid url", url: "::not a url", wantErr: "invalid URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.timeout)
				defer cancel()
			}
			got := (&HTTP{URL: tt.url, Keyword: tt.keyword}).Check(ctx)
			if got.OK != tt.wantOK || got.Error != tt.wantErr {
				t.Fatalf("got ok=%v err=%q, want ok=%v err=%q", got.OK, got.Error, tt.wantOK, tt.wantErr)
			}
			if tt.code != 0 && got.StatusCode != tt.code {
				t.Fatalf("status = %d, want %d", got.StatusCode, tt.code)
			}
			if got.OK && got.Latency <= 0 {
				t.Fatal("latency not recorded")
			}
		})
	}
}

func TestHTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	// The default client does not trust the test certificate.
	got := (&HTTP{URL: srv.URL}).Check(context.Background())
	if got.OK || !strings.Contains(got.Error, "certificate") {
		t.Fatalf("untrusted cert: ok=%v err=%q", got.OK, got.Error)
	}

	// A trusting client records the expiry date.
	got = (&HTTP{URL: srv.URL, Client: srv.Client()}).Check(context.Background())
	if !got.OK || got.CertExpiry.IsZero() {
		t.Fatalf("trusted cert: ok=%v err=%q expiry=%v", got.OK, got.Error, got.CertExpiry)
	}
}

func TestTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	tests := []struct {
		name    string
		addr    string
		wantOK  bool
		wantErr string
	}{
		{"open port", ln.Addr().String(), true, ""},
		{"closed port", "127.0.0.1:1", false, "connection refused"},
		{"missing port", "127.0.0.1", false, "invalid host:port"},
		{"no such host", "no-such-host.invalid:80", false, "no such host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := (&TCP{Address: tt.addr}).Check(context.Background())
			if got.OK != tt.wantOK || got.Error != tt.wantErr {
				t.Fatalf("got ok=%v err=%q, want ok=%v err=%q", got.OK, got.Error, tt.wantOK, tt.wantErr)
			}
		})
	}
}

func TestDNS(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		expected string
		wantOK   bool
		wantErr  string
	}{
		{"localhost resolves", "localhost", "", true, ""},
		{"expected ip matches", "localhost", "127.0.0.1", true, ""},
		{"expected ip differs", "localhost", "203.0.113.9", false, "expected IP not found"},
		{"no such host", "no-such-host.invalid", "", false, "no such host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := (&DNS{Hostname: tt.host, ExpectedIP: tt.expected}).Check(context.Background())
			if got.OK != tt.wantOK || got.Error != tt.wantErr {
				t.Fatalf("got ok=%v err=%q, want ok=%v err=%q", got.OK, got.Error, tt.wantOK, tt.wantErr)
			}
		})
	}
}

func TestPing(t *testing.T) {
	got := (&Ping{Host: "127.0.0.1"}).Check(context.Background())
	if got.Error == "ping not permitted on this server" {
		t.Skip("unprivileged ping is not allowed here")
	}
	if !got.OK {
		t.Fatalf("ping localhost: err=%q", got.Error)
	}
	if got := (&Ping{Host: "no-such-host.invalid"}).Check(context.Background()); got.OK || got.Error != "no such host" {
		t.Fatalf("ping unknown host: ok=%v err=%q", got.OK, got.Error)
	}
}

func TestNew(t *testing.T) {
	for _, typ := range []string{"http", "tcp", "ping", "dns"} {
		if _, err := New(Spec{Type: typ, Target: "x"}); err != nil {
			t.Errorf("New(%s): %v", typ, err)
		}
	}
	if _, err := New(Spec{Type: "push"}); err == nil {
		t.Error("New(push) should fail: push has no checker")
	}
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{nil, ""},
		{context.DeadlineExceeded, "timeout"},
		{&net.DNSError{IsNotFound: true}, "no such host"},
		{&net.DNSError{IsTimeout: true}, "DNS timeout"},
		{errors.New("dial tcp: something odd happened"), "something odd happened"},
	}
	for _, tt := range tests {
		if got := Describe(tt.err); got != tt.want {
			t.Errorf("Describe(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}
