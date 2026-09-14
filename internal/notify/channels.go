package notify

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// --- Slack ---

type slack struct {
	url    string
	client *http.Client
}

func (s *slack) Send(ctx context.Context, m Message) error {
	attachment := map[string]any{
		"color":    m.Color(),
		"title":    m.Title(),
		"text":     strings.Join(m.Lines(), "\n"),
		"fallback": m.Text(),
		"footer":   m.Brand,
	}
	if m.URL != "" {
		attachment["title_link"] = m.URL
	}
	return postJSON(ctx, s.client, s.url, map[string]any{"attachments": []any{attachment}}, nil)
}

// --- Discord ---

type discord struct {
	url    string
	client *http.Client
}

func (d *discord) Send(ctx context.Context, m Message) error {
	color, _ := strconv.ParseInt(strings.TrimPrefix(m.Color(), "#"), 16, 64)
	embed := map[string]any{
		"title":       m.Title(),
		"description": strings.Join(m.Lines(), "\n"),
		"color":       color,
		"footer":      map[string]any{"text": m.Brand},
	}
	if m.URL != "" {
		embed["url"] = m.URL
	}
	return postJSON(ctx, d.client, d.url, map[string]any{"embeds": []any{embed}}, nil)
}

// --- Telegram ---

// telegramAPI is the Bot API base. Tests replace it.
var telegramAPI = "https://api.telegram.org"

type telegram struct {
	token  string
	chatID string
	client *http.Client
}

func (t *telegram) Send(ctx context.Context, m Message) error {
	body := map[string]any{
		"chat_id":                  t.chatID,
		"text":                     m.Text(),
		"disable_web_page_preview": true,
	}
	return postJSON(ctx, t.client, telegramAPI+"/bot"+t.token+"/sendMessage", body, nil)
}

// --- ntfy ---

type ntfy struct {
	topicURL string
	token    string
	client   *http.Client
}

func (n *ntfy) Send(ctx context.Context, m Message) error {
	u, err := url.Parse(n.topicURL)
	if err != nil || u.Host == "" {
		return errors.New("invalid topic URL")
	}
	topic := strings.Trim(u.Path, "/")
	if topic == "" {
		return errors.New("the topic URL has no topic")
	}
	// The JSON form of the publish API takes UTF-8 titles. Headers do not.
	body := map[string]any{
		"topic":    topic,
		"title":    m.Title(),
		"message":  strings.Join(m.Lines(), "\n"),
		"priority": 3,
	}
	if m.Kind == KindDown {
		body["priority"] = 4
	}
	if m.URL != "" {
		body["click"] = m.URL
	}
	if strings.Join(m.Lines(), "") == "" {
		body["message"] = m.Title()
		delete(body, "title")
	}
	var headers map[string]string
	if n.token != "" {
		headers = map[string]string{"Authorization": "Bearer " + n.token}
	}
	base := u.Scheme + "://" + u.Host + "/"
	return postJSON(ctx, n.client, base, body, headers)
}

// --- Pushover ---

// pushoverAPI is the Pushover API base. Tests replace it.
var pushoverAPI = "https://api.pushover.net"

type pushover struct {
	user   string
	token  string
	repeat bool // DOWN alerts use emergency priority
	retry  int  // minutes between repeats
	expire int  // minutes until the repeats stop
	client *http.Client
}

func (p *pushover) Send(ctx context.Context, m Message) error {
	body := map[string]any{
		"token":    p.token,
		"user":     p.user,
		"title":    m.Title(),
		"message":  strings.Join(m.Lines(), "\n"),
		"priority": 0,
	}
	// Emergency priority repeats the alert until the user acknowledges it.
	// Only DOWN alerts get it. UP alerts and certificate warnings must not
	// keep a phone ringing.
	if p.repeat && m.Kind == KindDown {
		body["priority"] = 2
		body["retry"] = p.retry * 60
		body["expire"] = p.expire * 60
	}
	if m.URL != "" {
		body["url"] = m.URL
	}
	if strings.Join(m.Lines(), "") == "" {
		body["message"] = m.Title()
		delete(body, "title")
	}
	return postJSON(ctx, p.client, pushoverAPI+"/1/messages.json", body, nil)
}

// --- Webhook ---

type webhook struct {
	url    string
	client *http.Client
}

// webhookPayload is the JSON body from SPEC.md section 7.3.
type webhookPayload struct {
	Event   Kind           `json:"event"`
	Monitor webhookMonitor `json:"monitor"`
	Reason  string         `json:"reason,omitempty"`
	At      string         `json:"at"`
	DownFor int64          `json:"down_for_seconds,omitempty"`
	Expiry  string         `json:"cert_expiry,omitempty"`
	URL     string         `json:"url,omitempty"`
}

type webhookMonitor struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Target string `json:"target"`
}

func (w *webhook) Send(ctx context.Context, m Message) error {
	p := webhookPayload{
		Event:   m.Kind,
		Monitor: webhookMonitor{ID: m.Monitor.ID, Name: m.Monitor.Name, Type: m.Monitor.Type, Target: m.Monitor.Target},
		Reason:  m.Reason,
		At:      m.At.UTC().Format(time.RFC3339),
		URL:     m.URL,
	}
	if m.Kind == KindUp && m.DownFor > 0 {
		p.DownFor = int64(m.DownFor.Seconds())
	}
	if m.Kind == KindCert {
		p.Expiry = m.CertExpiry.UTC().Format(time.RFC3339)
	}
	return postJSON(ctx, w.client, w.url, p, nil)
}
