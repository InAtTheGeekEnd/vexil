package check

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

// TestHostileResponseReason serves HTTP responses that net/http cannot parse
// and that carry text the server chose. The reason goes to alerts and the
// public status page, so it must be a fixed string.
func TestHostileResponseReason(t *testing.T) {
	tests := []struct{ name, response string }{
		{"header line without a colon", "HTTP/1.1 200 OK\r\nSite moved - reset your password at evil.example\r\n\r\n"},
		{"status line with a colon", "HTTP/1.1 OK: Site moved - reset your password at evil.example\r\n\r\n"},
		{"bad status code", "HTTP/1.1 2x0 visit: evil.example\r\n\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { ln.Close() })
			go func() {
				for {
					conn, err := ln.Accept()
					if err != nil {
						return
					}
					go func() {
						defer conn.Close()
						r := bufio.NewReader(conn)
						for {
							line, err := r.ReadString('\n')
							if err != nil || line == "\r\n" {
								break
							}
						}
						_, _ = conn.Write([]byte(tt.response))
					}()
				}
			}()
			got := (&HTTP{URL: "http://" + ln.Addr().String() + "/"}).Check(context.Background())
			if got.OK || got.Error != "invalid HTTP response" {
				t.Fatalf("Check = %+v, want the reason %q", got, "invalid HTTP response")
			}
		})
	}
}

// TestDescribeCleansText checks the fallback for errors that Describe does
// not know: control and format characters go, and the text is cut to 60
// characters without a broken letter.
func TestDescribeCleansText(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"plain", errors.New("read: something odd"), "something odd"},
		{"control characters", errors.New("read: bad\x1b[31m\r\nthing\x00here"), "bad [31m thing here"},
		{"bidi override", errors.New("read: abc\u202edef"), "abc def"},
		{"invalid UTF-8", errors.New("read: ab\xffcd"), "ab cd"},
		{"long accented text", errors.New("read: " + strings.Repeat("é", 100)), strings.Repeat("é", 60)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Describe(tt.err); got != tt.want {
				t.Fatalf("Describe = %q, want %q", got, tt.want)
			}
		})
	}
}
