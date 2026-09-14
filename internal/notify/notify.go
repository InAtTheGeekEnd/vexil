// Package notify sends alerts to the seven channel types from SPEC.md
// section 7: email, Slack, Discord, Telegram, ntfy, Pushover and a plain
// webhook.
package notify

import (
	"context"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// Kind is the alert kind. The values are the webhook "event" names.
type Kind string

// Alert kinds.
const (
	KindDown Kind = "down"
	KindUp   Kind = "up"
	KindCert Kind = "cert_expiring"
	KindTest Kind = "test" // the Send test button
)

// Message is one alert, ready to render for any channel.
type Message struct {
	Kind       Kind
	Monitor    store.Monitor
	Reason     string        // DOWN: the check error
	At         time.Time     // when the alert happened
	DownFor    time.Duration // UP: length of the incident, 0 when unknown
	Started    time.Time     // DOWN and UP: start of the incident, zero when unknown
	CertExpiry time.Time     // KindCert
	URL        string        // link to the detail page, "" without a base URL
	Brand      string        // product name for the test title and footers
}

// Title is the first line: "🔴 API is down".
func (m Message) Title() string {
	switch m.Kind {
	case KindDown:
		return "🔴 " + m.Monitor.Name + " is down"
	case KindUp:
		return "🟢 " + m.Monitor.Name + " is up again"
	case KindCert:
		return fmt.Sprintf("🟡 The TLS certificate for %s expires in %s (%s)",
			m.Host(), plural(m.certDays(), "day"), m.CertExpiry.UTC().Format("2 Jan 2006"))
	case KindTest:
		return "🔔 Test message from " + m.Brand
	}
	return m.Monitor.Name
}

// Lines are the body lines after the title, without the link.
func (m Message) Lines() []string {
	switch m.Kind {
	case KindDown:
		reason := m.Reason
		if reason == "" {
			reason = "unknown"
		}
		return []string{"Reason: " + reason, "Since: " + m.At.UTC().Format("15:04 UTC")}
	case KindUp:
		if m.DownFor > 0 {
			return []string{"Down for: " + downFor(m.DownFor)}
		}
	case KindTest:
		return []string{"This channel works."}
	}
	return nil
}

// Text is the plain-text message: title, lines and link.
func (m Message) Text() string {
	parts := append([]string{m.Title()}, m.Lines()...)
	if m.URL != "" {
		parts = append(parts, m.URL)
	}
	return strings.Join(parts, "\n")
}

// Color is the status color as a hex string.
func (m Message) Color() string {
	switch m.Kind {
	case KindDown:
		return "#DC2626"
	case KindUp:
		return "#16A34A"
	case KindTest:
		return "#4F46E5"
	}
	return "#D97706"
}

// Host is the host name of the monitor target.
func (m Message) Host() string {
	if u, err := url.Parse(m.Monitor.Target); err == nil && u.Host != "" {
		return u.Hostname()
	}
	return m.Monitor.Target
}

func (m Message) certDays() int {
	return int(m.CertExpiry.Sub(m.At).Hours()/24 + 0.5)
}

// downFor formats an incident length: "4m 12s", "1h 3m", "2d 5h".
func downFor(d time.Duration) string {
	d = d.Round(time.Second)
	days := int(d.Hours() / 24)
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	secs := int(d.Seconds()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm %ds", mins, secs)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Sender delivers one message to one channel.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// canceler is a Sender that can stop the repeats of a DOWN alert when the
// monitor recovers.
type canceler interface {
	Cancel(ctx context.Context, m Message) error
}

// New returns the sender for a channel. The config must be valid.
func New(c store.Channel, client *http.Client) (Sender, error) {
	if client == nil {
		client = http.DefaultClient
	}
	cfg := c.Config
	switch c.Type {
	case store.ChannelEmail:
		return &email{host: cfg["host"], port: cfg["port"], username: cfg["username"], password: cfg["password"],
			from: cfg["from"], to: cfg["to"], implicitTLS: cfg["port"] == "465"}, nil
	case store.ChannelSlack:
		return &slack{url: cfg["url"], client: client}, nil
	case store.ChannelDiscord:
		return &discord{url: cfg["url"], client: client}, nil
	case store.ChannelTelegram:
		return &telegram{token: cfg["token"], chatID: cfg["chat_id"], client: client}, nil
	case store.ChannelNtfy:
		return &ntfy{topicURL: cfg["url"], token: cfg["token"], client: client}, nil
	case store.ChannelPushover:
		retry, _ := strconv.Atoi(cfg["retry"])
		expire, _ := strconv.Atoi(cfg["expire"])
		return &pushover{user: cfg["user"], token: cfg["token"], repeat: cfg["repeat"] == "1",
			retry: retry, expire: expire, client: client}, nil
	case store.ChannelWebhook:
		return &webhook{url: cfg["url"], client: client}, nil
	}
	return nil, fmt.Errorf("unknown channel type %q", c.Type)
}

// Field describes one config field of a channel type. The web form renders
// them and Validate checks them.
type Field struct {
	Key         string
	Label       string
	Type        string // text, url, email, number, password, switch
	Placeholder string
	Hint        string
	Default     string // the value of a new form
	Min, Max    int    // the range of a number
	Needs       string // the key of a switch that must be on for the field to count
	Required    bool
	Secret      bool // never shown again after save
}

// Types lists the channel types in display order with a label and a short
// description.
var Types = []struct {
	Type, Label, Description string
}{
	{store.ChannelEmail, "Email", "Through your SMTP server"},
	{store.ChannelSlack, "Slack", "Incoming webhook"},
	{store.ChannelDiscord, "Discord", "Channel webhook"},
	{store.ChannelTelegram, "Telegram", "A bot sends you a message"},
	{store.ChannelNtfy, "ntfy", "Push to your phone"},
	{store.ChannelPushover, "Pushover", "Push that can repeat"},
	{store.ChannelWebhook, "Webhook", "POST JSON to your own URL"},
}

// TypeLabel returns the display name of a channel type.
func TypeLabel(typ string) string {
	for _, t := range Types {
		if t.Type == typ {
			return t.Label
		}
	}
	return typ
}

var fields = map[string][]Field{
	store.ChannelEmail: {
		{Key: "host", Label: "SMTP host", Type: "text", Placeholder: "smtp.example.com", Required: true},
		{Key: "port", Label: "Port", Type: "number", Placeholder: "587", Hint: "587 uses STARTTLS. 465 uses TLS from the start.", Min: 1, Max: 65535, Required: true},
		{Key: "username", Label: "Username", Type: "text", Hint: "Leave empty if the server needs no login."},
		{Key: "password", Label: "Password", Type: "password", Secret: true},
		{Key: "from", Label: "From", Type: "email", Placeholder: "alerts@example.com", Required: true},
		{Key: "to", Label: "To", Type: "email", Placeholder: "you@example.com", Required: true},
	},
	store.ChannelSlack: {
		{Key: "url", Label: "Webhook URL", Type: "url", Placeholder: "https://hooks.slack.com/services/…", Hint: "Create an incoming webhook in your Slack app settings.", Required: true, Secret: true},
	},
	store.ChannelDiscord: {
		{Key: "url", Label: "Webhook URL", Type: "url", Placeholder: "https://discord.com/api/webhooks/…", Hint: "Channel settings, Integrations, Webhooks.", Required: true, Secret: true},
	},
	store.ChannelTelegram: {
		{Key: "token", Label: "Bot token", Type: "password", Hint: "From @BotFather.", Required: true, Secret: true},
		{Key: "chat_id", Label: "Chat ID", Type: "text", Placeholder: "123456789", Hint: "Your user ID or a group ID. Send the bot a message first.", Required: true},
	},
	store.ChannelNtfy: {
		{Key: "url", Label: "Topic URL", Type: "url", Placeholder: "https://ntfy.sh/your-topic", Hint: "Subscribe to the same topic in the ntfy app.", Required: true},
		{Key: "token", Label: "Access token", Type: "password", Hint: "Only for protected topics.", Secret: true},
	},
	store.ChannelPushover: {
		{Key: "user", Label: "User key", Type: "password", Hint: "On your Pushover dashboard.", Required: true, Secret: true},
		{Key: "token", Label: "Application token", Type: "password", Hint: "Create an application on pushover.net to get one.", Required: true, Secret: true},
		{Key: "repeat", Label: "Repeat until acknowledged", Type: "switch", Hint: "DOWN alerts use emergency priority and repeat until you acknowledge them in the Pushover app."},
		{Key: "retry", Label: "Retry interval", Type: "number", Default: "1", Min: 1, Max: 60, Needs: "repeat", Hint: "Minutes between repeats.", Required: true},
		{Key: "expire", Label: "Expiry time", Type: "number", Default: "60", Min: 1, Max: 180, Needs: "repeat", Hint: "Minutes until the repeats stop.", Required: true},
	},
	store.ChannelWebhook: {
		{Key: "url", Label: "URL", Type: "url", Placeholder: "https://example.com/hooks/uptime", Hint: "Receives a JSON POST for every alert.", Required: true, Secret: true},
	},
}

// Fields returns the config fields of a channel type, nil for an unknown
// type.
func Fields(typ string) []Field {
	return fields[typ]
}

// Validate checks the config of a channel. It returns one error message per
// bad field, keyed by field, and an empty map when the config is good.
func Validate(c store.Channel) map[string]string {
	errs := map[string]string{}
	fs, ok := fields[c.Type]
	if !ok {
		errs["type"] = "Choose a channel type."
		return errs
	}
	for _, f := range fs {
		if f.Needs != "" && c.Config[f.Needs] != "1" {
			continue
		}
		v := strings.TrimSpace(c.Config[f.Key])
		if v == "" {
			if f.Required {
				errs[f.Key] = "Enter the " + strings.ToLower(f.Label) + "."
			}
			continue
		}
		switch f.Type {
		case "url":
			u, err := url.Parse(v)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				errs[f.Key] = "Enter a full URL that starts with http:// or https://."
			}
		case "email":
			if _, err := mail.ParseAddress(v); err != nil {
				errs[f.Key] = "Enter one email address."
			}
		case "number":
			n, err := strconv.Atoi(v)
			if err != nil || n < f.Min || n > f.Max {
				errs[f.Key] = fmt.Sprintf("Enter a number between %d and %d.", f.Min, f.Max)
			}
		}
	}
	return errs
}

// Secrets returns the secret values of a channel config, for redaction.
func Secrets(c store.Channel) []string {
	var out []string
	for _, f := range fields[c.Type] {
		if f.Secret && c.Config[f.Key] != "" {
			out = append(out, c.Config[f.Key])
		}
	}
	return out
}

// Summary describes where a channel sends to, without secrets.
func Summary(c store.Channel) string {
	cfg := c.Config
	switch c.Type {
	case store.ChannelEmail:
		return "To " + cfg["to"] + " through " + cfg["host"]
	case store.ChannelSlack, store.ChannelDiscord, store.ChannelWebhook:
		if u, err := url.Parse(cfg["url"]); err == nil && u.Host != "" {
			return "POST to " + u.Host
		}
		return "POST"
	case store.ChannelTelegram:
		return "Chat " + cfg["chat_id"]
	case store.ChannelNtfy:
		return strings.TrimPrefix(strings.TrimPrefix(cfg["url"], "https://"), "http://")
	case store.ChannelPushover:
		if cfg["repeat"] == "1" {
			return "DOWN alerts repeat until acknowledged"
		}
		return "Normal priority"
	}
	return ""
}
