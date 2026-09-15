package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/check"
	"github.com/InAtTheGeekEnd/vexil/internal/engine"
)

// readEvent reads one SSE event: its name and its data line.
func readEvent(t *testing.T, r *bufio.Reader) (name, data string) {
	t.Helper()
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read event: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "":
			if name != "" || data != "" {
				return name, data
			}
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case strings.HasPrefix(line, ":"), strings.HasPrefix(line, "retry: "):
			return line, ""
		}
	}
}

func TestEventsStream(t *testing.T) {
	old := sseHeartbeat
	sseHeartbeat = 50 * time.Millisecond
	t.Cleanup(func() { sseHeartbeat = old })

	s, st := newTestServer(t, Options{})
	ts, c := loggedIn(t, s, st)

	res := get(t, c, ts.URL+"/events")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	for k, want := range map[string]string{
		"Content-Type":      "text/event-stream",
		"Cache-Control":     "no-store",
		"X-Accel-Buffering": "no",
	} {
		if got := res.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	r := bufio.NewReader(res.Body)
	if line, _ := readEvent(t, r); line != "retry: 3000" {
		t.Fatalf("first line = %q, want the retry hint", line)
	}

	// A result with a state change sends a check event and a state event.
	s.engine.Hub().Publish(engine.Event{
		MonitorID: 7, Prev: engine.Up, State: engine.Down, At: time.Now(),
		Result: check.Result{OK: false, Error: "HTTP 503"},
	})
	name, data := readEvent(t, r)
	if name != "check" {
		t.Fatalf("event = %q, want check", name)
	}
	var ce checkEvent
	if err := json.Unmarshal([]byte(data), &ce); err != nil {
		t.Fatal(err)
	}
	if ce.ID != 7 || ce.OK || ce.Error != "HTTP 503" {
		t.Fatalf("check event = %+v", ce)
	}
	name, data = readEvent(t, r)
	if name != "state" {
		t.Fatalf("event = %q, want state", name)
	}
	var se stateEvent
	if err := json.Unmarshal([]byte(data), &se); err != nil {
		t.Fatal(err)
	}
	if se.ID != 7 || se.State != "down" || se.Down != 0 {
		t.Fatalf("state event = %+v", se)
	}

	// A result without a state change sends only a check event, and a
	// state change without a result (pause) sends only a state event.
	s.engine.Hub().Publish(engine.Event{MonitorID: 7, Prev: engine.Up, State: engine.Up, Result: check.Result{OK: true, Latency: 182 * time.Millisecond}})
	s.engine.Hub().Publish(engine.Event{MonitorID: 8, Prev: engine.Up, State: engine.Paused})
	if name, data = readEvent(t, r); name != "check" || !strings.Contains(data, `"latency":182`) {
		t.Fatalf("event = %q %s, want a check with latency 182", name, data)
	}
	if name, data = readEvent(t, r); name != "state" || !strings.Contains(data, `"state":"paused"`) {
		t.Fatalf("event = %q %s, want a paused state", name, data)
	}

	// Heartbeat comments arrive while nothing happens.
	if line, _ := readEvent(t, r); !strings.HasPrefix(line, ":") {
		t.Fatalf("line = %q, want a heartbeat comment", line)
	}

	// CloseEvents ends the stream.
	s.CloseEvents()
	done := make(chan error, 1)
	go func() {
		for {
			if _, err := r.ReadString('\n'); err != nil {
				done <- err
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stream still open after CloseEvents")
	}
	s.CloseEvents() // a second call is harmless
}

// TestEventsOutliveWriteTimeout runs the stream on a server with a short
// WriteTimeout, as vexil sets one. The stream clears its write deadline, so
// heartbeats must keep arriving after the timeout.
func TestEventsOutliveWriteTimeout(t *testing.T) {
	old := sseHeartbeat
	sseHeartbeat = 50 * time.Millisecond
	t.Cleanup(func() { sseHeartbeat = old })

	s, st := newTestServer(t, Options{})
	setPassword(t, st)
	token, hash, err := newSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.CreateSession(context.Background(), hash, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	const writeTimeout = 200 * time.Millisecond
	ts := httptest.NewUnstartedServer(s)
	ts.Config.WriteTimeout = writeTimeout
	ts.Start()
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	r := bufio.NewReader(res.Body)
	if line, _ := readEvent(t, r); line != "retry: 3000" {
		t.Fatalf("first line = %q, want the retry hint", line)
	}
	for end := time.Now().Add(3 * writeTimeout); time.Now().Before(end); {
		if line, _ := readEvent(t, r); !strings.HasPrefix(line, ":") {
			t.Fatalf("line = %q, want a heartbeat comment", line)
		}
	}
}

// TestEventsEndWhenSessionEnds opens a stream and then ends the session. The
// stream must close at the next heartbeat.
func TestEventsEndWhenSessionEnds(t *testing.T) {
	old := sseHeartbeat
	sseHeartbeat = 50 * time.Millisecond
	t.Cleanup(func() { sseHeartbeat = old })

	s, st := newTestServer(t, Options{})
	ts, c := loggedIn(t, s, st)
	res := get(t, c, ts.URL+"/events")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	r := bufio.NewReader(res.Body)
	if line, _ := readEvent(t, r); line != "retry: 3000" {
		t.Fatalf("first line = %q, want the retry hint", line)
	}
	if line, _ := readEvent(t, r); !strings.HasPrefix(line, ":") {
		t.Fatalf("line = %q, want a heartbeat while the session is valid", line)
	}

	setPassword(t, st) // a password change ends every session
	done := make(chan error, 1)
	go func() {
		for {
			if _, err := r.ReadString('\n'); err != nil {
				done <- err
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the stream is still open after the session ended")
	}
}

func TestEventsNeedLogin(t *testing.T) {
	s, st := newTestServer(t, Options{})
	setPassword(t, st)
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	// An EventSource cannot follow a redirect to the login page, so the
	// stream answers 401 and the page shows that its data is old.
	res := get(t, client(t), ts.URL+"/events")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous /events = %d, want 401", res.StatusCode)
	}

	// A session that ends, as on a password change, gets 401 too.
	ts2, c := loggedIn(t, s, st)
	setPassword(t, st)
	if res := get(t, c, ts2.URL+"/events"); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/events after the session ended = %d, want 401", res.StatusCode)
	}
}
