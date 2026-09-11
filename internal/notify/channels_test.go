package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// capture is a fake HTTP endpoint that records the last request.
type capture struct {
	mu      sync.Mutex
	path    string
	headers http.Header
	body    string
	status  int
	reply   string
}

func newCapture(t *testing.T) (*httptest.Server, *capture) {
	t.Helper()
	c := &capture{status: http.StatusOK}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.path, c.headers, c.body = r.URL.Path, r.Header.Clone(), string(b)
		status, reply := c.status, c.reply
		c.mu.Unlock()
		w.WriteHeader(status)
		io.WriteString(w, reply)
	}))
	t.Cleanup(ts.Close)
	return ts, c
}

func (c *capture) json(t *testing.T) map[string]any {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var out map[string]any
	if err := json.Unmarshal([]byte(c.body), &out); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, c.body)
	}
	return out
}

var downMsg = Message{Kind: KindDown, Monitor: testMon, Reason: "HTTP 503", At: testAt, URL: "https://status.example.com/monitors/12"}

func send(t *testing.T, c store.Channel, m Message) error {
	t.Helper()
	s, err := New(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s.Send(context.Background(), m)
}

func TestSlack(t *testing.T) {
	ts, cap := newCapture(t)
	if err := send(t, store.Channel{Type: store.ChannelSlack, Config: map[string]string{"url": ts.URL + "/services/x"}}, downMsg); err != nil {
		t.Fatal(err)
	}
	att := cap.json(t)["attachments"].([]any)[0].(map[string]any)
	if att["color"] != "#DC2626" || att["title"] != "🔴 API is down" || !strings.Contains(att["text"].(string), "Reason: HTTP 503") || att["title_link"] != downMsg.URL {
		t.Fatalf("attachment = %v", att)
	}
}

func TestDiscord(t *testing.T) {
	ts, cap := newCapture(t)
	cap.status = http.StatusNoContent
	if err := send(t, store.Channel{Type: store.ChannelDiscord, Config: map[string]string{"url": ts.URL + "/api/webhooks/1/x"}}, downMsg); err != nil {
		t.Fatal(err)
	}
	embed := cap.json(t)["embeds"].([]any)[0].(map[string]any)
	if embed["title"] != "🔴 API is down" || embed["color"].(float64) != 0xDC2626 || embed["url"] != downMsg.URL {
		t.Fatalf("embed = %v", embed)
	}
}

func TestTelegram(t *testing.T) {
	ts, cap := newCapture(t)
	old := telegramAPI
	telegramAPI = ts.URL
	t.Cleanup(func() { telegramAPI = old })
	c := store.Channel{Type: store.ChannelTelegram, Config: map[string]string{"token": "123:SECRET", "chat_id": "42"}}
	if err := send(t, c, downMsg); err != nil {
		t.Fatal(err)
	}
	if cap.path != "/bot123:SECRET/sendMessage" {
		t.Fatalf("path = %q", cap.path)
	}
	body := cap.json(t)
	if body["chat_id"] != "42" || !strings.HasPrefix(body["text"].(string), "🔴 API is down\n") {
		t.Fatalf("body = %v", body)
	}

	// A Telegram error carries a description. The token stays out of it.
	cap.status, cap.reply = http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`
	err := send(t, c, downMsg)
	if err == nil || err.Error() != "HTTP 400: Bad Request: chat not found" {
		t.Fatalf("err = %v", err)
	}
}

func TestNtfy(t *testing.T) {
	ts, cap := newCapture(t)
	c := store.Channel{Type: store.ChannelNtfy, Config: map[string]string{"url": ts.URL + "/alerts", "token": "tk_secret"}}
	if err := send(t, c, downMsg); err != nil {
		t.Fatal(err)
	}
	if cap.path != "/" || cap.headers.Get("Authorization") != "Bearer tk_secret" {
		t.Fatalf("path = %q, auth = %q", cap.path, cap.headers.Get("Authorization"))
	}
	body := cap.json(t)
	if body["topic"] != "alerts" || body["title"] != "🔴 API is down" || body["priority"].(float64) != 4 || body["click"] != downMsg.URL {
		t.Fatalf("body = %v", body)
	}

	// A message with no body lines goes into the message field.
	cert := Message{Kind: KindCert, Monitor: testMon, At: testAt, CertExpiry: testAt.Add(5 * 24 * time.Hour)}
	if err := send(t, c, cert); err != nil {
		t.Fatal(err)
	}
	body = cap.json(t)
	if _, has := body["title"]; has || !strings.HasPrefix(body["message"].(string), "🟡") || body["priority"].(float64) != 3 {
		t.Fatalf("cert body = %v", body)
	}

	// No token, no header.
	delete(c.Config, "token")
	if err := send(t, c, downMsg); err != nil {
		t.Fatal(err)
	}
	if cap.headers.Get("Authorization") != "" {
		t.Fatal("Authorization header sent without a token")
	}
}

func TestWebhook(t *testing.T) {
	ts, cap := newCapture(t)
	c := store.Channel{Type: store.ChannelWebhook, Config: map[string]string{"url": ts.URL + "/hook?key=SECRET"}}
	if err := send(t, c, downMsg); err != nil {
		t.Fatal(err)
	}
	var p webhookPayload
	if err := json.Unmarshal([]byte(cap.body), &p); err != nil {
		t.Fatal(err)
	}
	if p.Event != KindDown || p.Monitor.ID != 12 || p.Monitor.Name != "API" || p.Monitor.Type != "http" ||
		p.Monitor.Target != testMon.Target || p.Reason != "HTTP 503" || p.At != "2026-09-11T14:02:00Z" || p.URL != downMsg.URL {
		t.Fatalf("payload = %+v", p)
	}
	if cap.headers.Get("Content-Type") != "application/json" {
		t.Fatalf("content type = %q", cap.headers.Get("Content-Type"))
	}

	up := Message{Kind: KindUp, Monitor: testMon, At: testAt, DownFor: 252 * time.Second}
	if err := send(t, c, up); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cap.body, `"event":"up"`) || !strings.Contains(cap.body, `"down_for_seconds":252`) || strings.Contains(cap.body, `"reason"`) {
		t.Fatalf("up payload = %s", cap.body)
	}

	// Errors carry the status, never the URL.
	cap.status, cap.reply = http.StatusForbidden, "<html>forbidden</html>"
	err := send(t, c, downMsg)
	if err == nil || err.Error() != "HTTP 403" {
		t.Fatalf("err = %v", err)
	}
	ts.Close()
	err = send(t, c, downMsg)
	if err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("err after close = %v", err)
	}
}
