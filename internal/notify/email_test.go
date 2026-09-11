package notify

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// fakeSMTP is a minimal SMTP server for one session. mode is "plain",
// "starttls" or "tls".
type fakeSMTP struct {
	mode     string
	cert     tls.Certificate
	data     chan string // the DATA body
	authSeen chan string
}

func newFakeSMTP(t *testing.T, mode string) (*fakeSMTP, string) {
	t.Helper()
	f := &fakeSMTP{mode: mode, cert: selfSigned(t), data: make(chan string, 1), authSeen: make(chan string, 1)}
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
			go f.serve(conn)
		}
	}()
	return f, ln.Addr().String()
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	if f.mode == "tls" {
		conn = tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{f.cert}})
	}
	r := bufio.NewReader(conn)
	w := func(s string) { conn.Write([]byte(s + "\r\n")) }
	w("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			w("250-fake")
			if f.mode == "starttls" {
				w("250-STARTTLS")
			}
			w("250 AUTH PLAIN")
		case cmd == "STARTTLS":
			w("220 go ahead")
			tc := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{f.cert}})
			if err := tc.Handshake(); err != nil {
				return
			}
			conn = tc
			r = bufio.NewReader(conn)
		case strings.HasPrefix(cmd, "AUTH"):
			f.authSeen <- strings.TrimSpace(line)
			w("235 ok")
		case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
			w("250 ok")
		case cmd == "DATA":
			w("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.data <- b.String()
			w("250 queued")
		case cmd == "QUIT":
			w("221 bye")
			return
		default:
			w("500 what")
		}
	}
}

func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestEmail(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		username string
		wantErr  string
	}{
		{"plain without login", "plain", "", ""},
		{"plain refuses a login", "plain", "user", "does not offer STARTTLS"},
		{"starttls with login", "starttls", "user", ""},
		{"implicit tls on 465", "tls", "user", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, addr := newFakeSMTP(t, tc.mode)
			host, port, _ := net.SplitHostPort(addr)
			e := &email{host: host, port: port, username: tc.username, password: "pw", from: "a@example.com", to: "b@example.com",
				implicitTLS: tc.mode == "tls", tlsConfig: &tls.Config{InsecureSkipVerify: true}}
			err := e.Send(context.Background(), downMsg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case body := <-f.data:
				for _, want := range []string{"Subject: =?utf-8?q?", "multipart/alternative", "text/plain", "text/html", "Reason: HTTP 503", "background:#DC2626", "Sent by"} {
					if !strings.Contains(body, want) {
						t.Errorf("mail lacks %q", want)
					}
				}
			case <-time.After(2 * time.Second):
				t.Fatal("no mail received")
			}
			if tc.username != "" {
				select {
				case auth := <-f.authSeen:
					if !strings.HasPrefix(auth, "AUTH PLAIN") {
						t.Fatalf("auth = %q", auth)
					}
				default:
					t.Fatal("no AUTH sent")
				}
			}
		})
	}
}

func TestEmailPort465IsImplicitTLS(t *testing.T) {
	s, err := New(store.Channel{Type: store.ChannelEmail, Config: map[string]string{"host": "h", "port": "465"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !s.(*email).implicitTLS {
		t.Fatal("port 465 does not use implicit TLS")
	}
	s, _ = New(store.Channel{Type: store.ChannelEmail, Config: map[string]string{"host": "h", "port": "587"}}, nil)
	if s.(*email).implicitTLS {
		t.Fatal("port 587 uses implicit TLS")
	}
}
