package check

import (
	"context"
	"net"
	"testing"
	"time"
)

// TestReadableErrors runs HTTP checks against servers that fail in ways
// Describe used to show as jargon ("EOF") or as a wrong reason ("invalid
// certificate"). SPEC.md section 6.4 asks for errors a non-expert can read.
func TestReadableErrors(t *testing.T) {
	tests := []struct {
		name    string
		scheme  string
		keyword string
		serve   func(net.Conn)
		want    string
	}{
		{"server closes the connection", "http", "", func(c net.Conn) {
			readSome(c)
		}, "connection closed"},
		{"body ends early", "http", "needle", func(c net.Conn) {
			readSome(c)
			c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\nshort"))
		}, "connection closed"},
		{"https to a port that does not speak TLS", "https", "", func(c net.Conn) {
			readSome(c)
			c.Write([]byte("SSH-2.0-OpenSSH_9.6\r\n"))
		}, "not an HTTPS server"},
		{"TLS handshake refused", "https", "", func(c net.Conn) {
			readSome(c)
			// A TLS alert record: fatal, handshake_failure.
			c.Write([]byte{0x15, 0x03, 0x01, 0x00, 0x02, 0x02, 0x28})
		}, "TLS handshake failed"},
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
						tt.serve(conn)
					}()
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			got := (&HTTP{URL: tt.scheme + "://" + ln.Addr().String() + "/", Keyword: tt.keyword}).Check(ctx)
			if got.OK || got.Error != tt.want {
				t.Fatalf("Check = %+v, want the error %q", got, tt.want)
			}
		})
	}
}

// readSome reads the start of what the client sends: a request line or a
// TLS ClientHello.
func readSome(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	_, _ = c.Read(buf)
}
