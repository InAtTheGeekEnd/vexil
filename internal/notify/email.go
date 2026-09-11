package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/check"
)

type email struct {
	host, port         string
	username, password string
	from, to           string
	implicitTLS        bool        // port 465: TLS from the first byte
	tlsConfig          *tls.Config // nil means verify the server certificate
}

// Send delivers the message over SMTP. Port 465 speaks TLS from the start.
// Every other port starts plain and upgrades with STARTTLS when the server
// offers it. A login over a plain connection is refused.
func (e *email) Send(ctx context.Context, m Message) error {
	d := net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(e.host, e.port))
	if err != nil {
		return errors.New(check.Describe(err))
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	tlsCfg := e.tlsConfig
	if tlsCfg == nil {
		tlsCfg = &tls.Config{ServerName: e.host, MinVersion: tls.VersionTLS12}
	}
	if e.implicitTLS {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return errors.New(check.Describe(err))
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, e.host)
	if err != nil {
		return smtpError(err)
	}
	defer c.Close()
	if !e.implicitTLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				return errors.New(check.Describe(err))
			}
		} else if e.username != "" {
			return errors.New("the server does not offer STARTTLS, so the login cannot be sent")
		}
	}
	if e.username != "" {
		if err := c.Auth(smtp.PlainAuth("", e.username, e.password, e.host)); err != nil {
			return smtpError(err)
		}
	}
	if err := c.Mail(e.from); err != nil {
		return smtpError(err)
	}
	if err := c.Rcpt(e.to); err != nil {
		return smtpError(err)
	}
	w, err := c.Data()
	if err != nil {
		return smtpError(err)
	}
	if _, err := w.Write(e.body(m)); err != nil {
		return smtpError(err)
	}
	if err := w.Close(); err != nil {
		return smtpError(err)
	}
	return c.Quit()
}

// smtpError keeps the server's reply, which is short, and shortens
// everything else.
func smtpError(err error) error {
	msg := err.Error()
	if len(msg) > 3 && msg[0] >= '4' && msg[0] <= '5' && msg[3] == ' ' {
		if len(msg) > 100 {
			msg = msg[:100]
		}
		return errors.New(msg)
	}
	return errors.New(check.Describe(err))
}

// body builds a multipart/alternative mail with a text and an HTML part.
func (e *email) body(m Message) []byte {
	var b bytes.Buffer
	boundary := "vx" + fmt.Sprintf("%d", m.At.UnixNano())
	fmt.Fprintf(&b, "From: %s\r\n", e.from)
	fmt.Fprintf(&b, "To: %s\r\n", e.to)
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", m.Title()))
	fmt.Fprintf(&b, "Date: %s\r\n", m.At.Format(time.RFC1123Z))
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n\r\n", boundary, crlf(m.Text()))
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n", boundary)
	var html bytes.Buffer
	_ = emailTemplate.Execute(&html, m)
	b.WriteString(crlf(html.String()))
	fmt.Fprintf(&b, "\r\n--%s--\r\n", boundary)
	return b.Bytes()
}

func crlf(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}

// emailTemplate is the simple branded HTML part. Inline styles only: mail
// clients drop style sheets.
var emailTemplate = template.Must(template.New("email").Parse(`<!doctype html>
<html><body style="margin:0;padding:24px;background:#FAFAFA;font-family:Inter,-apple-system,Segoe UI,Roboto,sans-serif;color:#0F1115">
<table role="presentation" cellpadding="0" cellspacing="0" style="max-width:520px;margin:0 auto;background:#FFFFFF;border:1px solid #E8EAED;border-radius:12px;overflow:hidden">
<tr><td style="height:4px;background:{{.Color}}"></td></tr>
<tr><td style="padding:24px">
<p style="margin:0 0 12px;font-size:20px;font-weight:600">{{.Title}}</p>
{{range .Lines}}<p style="margin:0 0 6px;font-size:14px;color:#6B7280">{{.}}</p>{{end}}
{{if .URL}}<p style="margin:16px 0 0"><a href="{{.URL}}" style="display:inline-block;padding:8px 14px;background:#6366F1;color:#FFFFFF;text-decoration:none;border-radius:8px;font-size:14px;font-weight:600">Open {{.Monitor.Name}}</a></p>{{end}}
</td></tr>
<tr><td style="padding:12px 24px;border-top:1px solid #E8EAED;font-size:12px;color:#6B7280">Sent by {{.Brand}}</td></tr>
</table>
</body></html>
`))
