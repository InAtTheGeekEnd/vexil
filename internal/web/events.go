package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/check"
	"github.com/InAtTheGeekEnd/vexil/internal/engine"
)

// sseHeartbeat is the time between comment lines. They keep proxies and
// browsers from closing a quiet stream. Tests shorten it.
var sseHeartbeat = 25 * time.Second

// checkEvent is the data of a "check" event: one result.
type checkEvent struct {
	ID      int64  `json:"id"`
	OK      bool   `json:"ok"`
	Latency int64  `json:"latency"`
	Error   string `json:"error,omitempty"`
}

// stateEvent is the data of a "state" event: a new state plus the number of
// monitors that are down, for the tab title and the favicon. For a DOWN
// state, Since is the start of the open incident in unix seconds: the page
// orders the strip by it.
type stateEvent struct {
	ID    int64  `json:"id"`
	State string `json:"state"`
	Down  int    `json:"down"`
	Since int64  `json:"since,omitempty"`
}

// handleEvents streams engine events as server-sent events. It ends when
// the client leaves or when CloseEvents runs.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		http.Error(w, "the engine is not running", http.StatusServiceUnavailable)
		return
	}
	rc := http.NewResponseController(w)
	events, unsubscribe := s.engine.Hub().Subscribe(64)
	defer unsubscribe()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(w, "retry: 3000\n\n"); err != nil {
		return
	}
	if err := rc.Flush(); err != nil {
		return
	}

	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.closing:
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
		case ev := <-events:
			if err := s.writeEvent(r.Context(), w, ev); err != nil {
				return
			}
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

// writeEvent sends a "check" event for a result and a "state" event for a
// state change. An event can carry both.
func (s *Server) writeEvent(ctx context.Context, w http.ResponseWriter, ev engine.Event) error {
	if ev.Result != (check.Result{}) {
		data := checkEvent{ID: ev.MonitorID, OK: ev.Result.OK, Latency: ev.Result.Latency.Milliseconds(), Error: ev.Result.Error}
		if err := writeSSE(w, "check", data); err != nil {
			return err
		}
	}
	if ev.State != ev.Prev {
		data := stateEvent{ID: ev.MonitorID, State: stateClass(ev.State), Down: s.downCount()}
		if ev.State == engine.Down {
			// The engine opens the incident before it publishes the event.
			since, err := s.downSince(ctx, ev.MonitorID)
			if err != nil {
				s.log.Error("read the open incident", "monitor", ev.MonitorID, "err", err)
			}
			data.Since = since
		}
		if err := writeSSE(w, "state", data); err != nil {
			return err
		}
	}
	return nil
}

func writeSSE(w http.ResponseWriter, name string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
	return err
}

// downCount returns the number of monitors that are down.
func (s *Server) downCount() int {
	if s.engine == nil {
		return 0
	}
	var n int
	for _, st := range s.engine.Statuses() {
		if st.State == engine.Down {
			n++
		}
	}
	return n
}

// CloseEvents ends every open SSE stream at once. Call it on shutdown
// before http.Server.Shutdown, which waits for open requests.
func (s *Server) CloseEvents() {
	s.closeOnce.Do(func() { close(s.closing) })
}
