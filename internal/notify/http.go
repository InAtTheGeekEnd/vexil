package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/InAtTheGeekEnd/vexil/internal/check"
)

// postJSON sends a JSON body. It returns a short error without the URL
// when the request fails or the response is not 2xx.
func postJSON(ctx context.Context, client *http.Client, target string, body any, headers map[string]string) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(b))
	if err != nil {
		return errors.New("invalid URL")
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	if err != nil {
		return describe(err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}
	return httpError(res)
}

// httpError turns a bad response into "HTTP 403" or "HTTP 400: chat not
// found" when the body carries a short description.
func httpError(res *http.Response) error {
	msg := fmt.Sprintf("HTTP %d", res.StatusCode)
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	var detail struct {
		Description string   `json:"description"` // Telegram
		Message     string   `json:"message"`     // Discord
		Error       string   `json:"error"`       // ntfy
		Errors      []string `json:"errors"`      // Pushover
	}
	text := ""
	if json.Unmarshal(body, &detail) == nil {
		text = firstNonEmpty(detail.Description, detail.Message, detail.Error)
		if text == "" && len(detail.Errors) > 0 {
			text = detail.Errors[0]
		}
	} else if t := strings.TrimSpace(string(body)); t != "" && !strings.HasPrefix(t, "<") {
		text = t // Slack answers with plain text like "no_service"
	}
	if text != "" {
		if len(text) > 80 {
			text = text[:80]
		}
		msg += ": " + text
	}
	return errors.New(msg)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// describe turns a transport error into a short message. Go puts the full
// URL into *url.Error, and webhook URLs carry tokens, so the URL is dropped.
func describe(err error) error {
	if err == nil {
		return nil
	}
	var uerr *url.Error
	if errors.As(err, &uerr) {
		err = uerr.Err
	}
	return errors.New(check.Describe(err))
}

// Redact replaces every secret in msg with bullets. Use it on any text that
// reaches a log line or the UI.
func Redact(msg string, secrets []string) string {
	for _, s := range secrets {
		if len(s) >= 4 {
			msg = strings.ReplaceAll(msg, s, "••••")
		}
	}
	return msg
}
