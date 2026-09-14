package check

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	maxRedirects = 10
	keywordLimit = 1 << 20 // first 1 MB of the body
)

// HTTP checks that a URL answers with status 200 to 299 and, if set,
// that the body contains Keyword.
type HTTP struct {
	URL     string
	Keyword string
	// Client overrides the HTTP client. Nil uses a default client.
	Client *http.Client
}

var errTooManyRedirects = errors.New("too many redirects")

var defaultClient = &http.Client{
	// Every check opens a new connection. A reused one keeps the old
	// certificate and DNS answer, and its latency leaves out the connect and
	// the TLS handshake.
	Transport: newCheckTransport(),
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return errTooManyRedirects
		}
		return nil
	},
}

// newCheckTransport is the default transport without keep-alive.
func newCheckTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DisableKeepAlives = true
	return t
}

func (h *HTTP) Check(ctx context.Context) Result {
	client := h.Client
	if client == nil {
		client = defaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.URL, nil)
	if err != nil {
		return Result{Error: "invalid URL"}
	}
	req.Header.Set("User-Agent", "uptime-check/1.0")
	req.Header.Set("Accept", "*/*")

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, errTooManyRedirects) {
			return Result{Error: "too many redirects"}
		}
		return fail(err)
	}
	defer resp.Body.Close()

	res := Result{StatusCode: resp.StatusCode}
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		res.CertExpiry = resp.TLS.PeerCertificates[0].NotAfter
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		res.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		res.Latency = time.Since(start)
		return res
	}
	if h.Keyword != "" {
		body, err := io.ReadAll(io.LimitReader(resp.Body, keywordLimit))
		if err != nil {
			res.Error = Describe(err)
			return res
		}
		if !strings.Contains(string(body), h.Keyword) {
			res.Error = "keyword not found"
			res.Latency = time.Since(start)
			return res
		}
	}
	res.Latency = time.Since(start)
	res.OK = true
	return res
}
