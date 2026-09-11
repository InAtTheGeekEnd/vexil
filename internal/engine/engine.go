// Package engine schedules checks, tracks monitor state and writes results.
package engine

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/check"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

const (
	maxConcurrent   = 50
	shutdownWait    = 10 * time.Second
	pushGraceFactor = 2 // a push monitor fails after 2 x interval without a push
)

// Notifier receives alerts. Milestone 6 adds the real channels.
type Notifier interface {
	Notify(ctx context.Context, ev Event)
}

type noopNotifier struct{}

func (noopNotifier) Notify(context.Context, Event) {}

// Options configures an Engine.
type Options struct {
	Log      *slog.Logger // nil means slog.Default()
	Notifier Notifier     // nil means no alerts
}

type result struct {
	monitorID int64
	res       check.Result
	at        time.Time
}

type runner struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// Engine runs one goroutine per monitor and one writer goroutine.
type Engine struct {
	store    *store.Store
	log      *slog.Logger
	hub      *Hub
	notifier Notifier

	// newChecker and interval are replaced in tests.
	newChecker func(store.Monitor) (check.Checker, error)
	interval   func(store.Monitor) time.Duration

	sem     chan struct{}
	results chan result
	stopCh  chan struct{}
	done    chan struct{}

	mu      sync.Mutex
	baseCtx context.Context
	cancel  context.CancelFunc
	runners map[int64]*runner
	status  map[int64]*Status
	started bool
	stopped bool
}

// New builds an engine. Call Start to run it.
func New(st *store.Store, opts Options) *Engine {
	e := &Engine{
		store:    st,
		log:      opts.Log,
		hub:      NewHub(),
		notifier: opts.Notifier,
		newChecker: func(m store.Monitor) (check.Checker, error) {
			return check.New(check.Spec{Type: m.Type, Target: m.Target, Keyword: m.Keyword, ExpectedIP: m.ExpectedIP})
		},
		interval: store.Monitor.Interval,
		sem:      make(chan struct{}, maxConcurrent),
		results:  make(chan result, 256),
		stopCh:   make(chan struct{}),
		done:     make(chan struct{}),
		runners:  make(map[int64]*runner),
		status:   make(map[int64]*Status),
	}
	if e.log == nil {
		e.log = slog.Default()
	}
	if e.notifier == nil {
		e.notifier = noopNotifier{}
	}
	return e
}

// Hub returns the event hub for SSE and tests.
func (e *Engine) Hub() *Hub { return e.hub }

// Ready reports nil while the engine runs.
func (e *Engine) Ready() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch {
	case e.stopped:
		return errors.New("stopped")
	case !e.started:
		return errors.New("not started")
	}
	return nil
}

// Start loads the monitors, restores their state and starts the goroutines.
func (e *Engine) Start(ctx context.Context) error {
	monitors, err := e.store.Monitors(ctx)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return errors.New("engine already started")
	}
	e.baseCtx, e.cancel = context.WithCancel(context.Background())
	for _, m := range monitors {
		st, err := e.initialStatus(ctx, m)
		if err != nil {
			return err
		}
		e.status[m.ID] = st
		if !m.Paused {
			e.startRunnerLocked(m)
		}
	}
	e.started = true
	go e.writer()
	e.log.Info("engine started", "monitors", len(monitors))
	return nil
}

// Stop cancels the tickers, waits for running checks (at most 10 seconds)
// and flushes the writer.
func (e *Engine) Stop() {
	e.mu.Lock()
	if !e.started || e.stopped {
		e.mu.Unlock()
		return
	}
	e.stopped = true
	e.cancel()
	runners := make([]*runner, 0, len(e.runners))
	for _, r := range e.runners {
		runners = append(runners, r)
	}
	e.mu.Unlock()

	deadline := time.After(shutdownWait)
	for _, r := range runners {
		select {
		case <-r.done:
		case <-deadline:
			e.log.Warn("engine stop: checks still running after timeout")
		}
	}
	close(e.stopCh)
	<-e.done
	e.log.Info("engine stopped")
}

// Reload restarts the goroutine of one monitor after an edit, pause,
// resume or delete. Call it after the store change.
func (e *Engine) Reload(ctx context.Context, id int64) error {
	m, err := e.store.Monitor(ctx, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	deleted := errors.Is(err, store.ErrNotFound)

	e.mu.Lock()
	r := e.runners[id]
	delete(e.runners, id)
	e.mu.Unlock()
	if r != nil {
		r.cancel()
		<-r.done
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if deleted {
		delete(e.status, id)
		return nil
	}
	prev := Pending
	if old := e.status[id]; old != nil {
		prev = old.State
	}
	st := e.status[id]
	if st == nil {
		st = &Status{State: Pending}
		e.status[id] = st
	}
	if m.Paused {
		st.State, st.Fails = Paused, 0
	} else {
		if prev == Paused || st.State == Paused {
			st.State, st.Fails = Pending, 0
		}
		if e.started && !e.stopped {
			e.startRunnerLocked(m)
		}
	}
	if st.State != prev {
		e.hub.Publish(Event{MonitorID: id, Prev: prev, State: st.State, At: time.Now()})
	}
	return nil
}

// Push records a heartbeat for the push monitor with this token.
func (e *Engine) Push(ctx context.Context, token string) error {
	m, err := e.store.MonitorByPushToken(ctx, token)
	if err != nil {
		return err
	}
	now := time.Now()
	e.mu.Lock()
	if st := e.status[m.ID]; st != nil {
		st.LastPush = now
	}
	e.mu.Unlock()
	e.enqueue(result{monitorID: m.ID, res: check.Result{OK: true}, at: now})
	return nil
}

// Status returns the live status of one monitor.
func (e *Engine) Status(id int64) (Status, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.status[id]
	if !ok {
		return Status{}, false
	}
	return *st, true
}

// Statuses returns a copy of every live status.
func (e *Engine) Statuses() map[int64]Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[int64]Status, len(e.status))
	for id, st := range e.status {
		out[id] = *st
	}
	return out
}

// initialStatus rebuilds the state of a monitor from the database.
func (e *Engine) initialStatus(ctx context.Context, m store.Monitor) (*Status, error) {
	st := &Status{State: Pending}
	if m.Type == store.TypePush {
		last, err := e.store.LastSuccess(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		st.LastPush = last
	}
	if m.Paused {
		st.State = Paused
		return st, nil
	}
	recent, err := e.store.RecentChecks(ctx, m.ID, failuresBeforeDown)
	if err != nil {
		return nil, err
	}
	if len(recent) == 0 {
		return st, nil
	}
	last := recent[0]
	st.Last = check.Result{OK: last.OK, Latency: time.Duration(last.LatencyMS) * time.Millisecond, StatusCode: last.StatusCode, Error: last.Error}
	st.LastAt = last.At
	if last.OK {
		st.State = Up
		return st, nil
	}
	for _, c := range recent {
		if !c.OK {
			st.Fails++
		} else {
			break
		}
	}
	if _, err := e.store.CurrentIncident(ctx, m.ID); err == nil {
		st.State = Down
		if st.Fails < failuresBeforeDown {
			st.Fails = failuresBeforeDown
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	} else if st.Fails >= failuresBeforeDown {
		st.State = Down
	} else {
		st.State = Up
	}
	return st, nil
}

// startRunnerLocked starts the goroutine for m. The caller holds e.mu.
func (e *Engine) startRunnerLocked(m store.Monitor) {
	ctx, cancel := context.WithCancel(e.baseCtx)
	r := &runner{cancel: cancel, done: make(chan struct{})}
	e.runners[m.ID] = r
	go e.run(ctx, m, r)
}

// run is the goroutine of one monitor: a random start offset, then a tick
// every interval.
func (e *Engine) run(ctx context.Context, m store.Monitor, r *runner) {
	defer close(r.done)

	interval := e.interval(m)
	if interval <= 0 {
		interval = time.Minute
	}
	tick := e.tickFunc(m, interval)
	if tick == nil {
		return
	}

	offset := time.Duration(rand.Int64N(int64(interval)))
	select {
	case <-time.After(offset):
	case <-ctx.Done():
		return
	}
	tick(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			tick(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// tickFunc returns what one tick does for a monitor.
func (e *Engine) tickFunc(m store.Monitor, interval time.Duration) func(context.Context) {
	if m.Type == store.TypePush {
		start := time.Now()
		return func(ctx context.Context) {
			e.mu.Lock()
			ref := start
			if st := e.status[m.ID]; st != nil && st.LastPush.After(ref) {
				ref = st.LastPush
			}
			e.mu.Unlock()
			if time.Since(ref) > pushGraceFactor*interval {
				e.enqueue(result{monitorID: m.ID, res: check.Result{Error: "no push received"}, at: time.Now()})
			}
		}
	}

	checker, err := e.newChecker(m)
	if err != nil {
		e.log.Error("monitor has no checker", "monitor", m.ID, "err", err)
		return nil
	}
	return func(ctx context.Context) {
		select {
		case e.sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-e.sem }()

		cctx, cancel := context.WithTimeout(ctx, check.Timeout)
		res := checker.Check(cctx)
		cancel()
		if ctx.Err() != nil {
			return // shutting down or reloading: drop the result
		}
		e.enqueue(result{monitorID: m.ID, res: res, at: time.Now()})
	}
}

func (e *Engine) enqueue(r result) {
	select {
	case e.results <- r:
	case <-e.stopCh:
	}
}

// writer is the only goroutine that writes check results to SQLite.
func (e *Engine) writer() {
	defer close(e.done)
	for {
		select {
		case r := <-e.results:
			e.handle(r)
		case <-e.stopCh:
			for {
				select {
				case r := <-e.results:
					e.handle(r)
				default:
					return
				}
			}
		}
	}
}

func (e *Engine) handle(r result) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	e.mu.Lock()
	st := e.status[r.monitorID]
	if st == nil || st.State == Paused {
		e.mu.Unlock()
		return
	}
	prev := st.State
	var alert Alert
	st.State, st.Fails, alert = transition(prev, st.Fails, r.res.OK)
	st.Last = r.res
	st.LastAt = r.at
	if !r.res.CertExpiry.IsZero() {
		st.CertExpiry = r.res.CertExpiry
	}
	ev := Event{MonitorID: r.monitorID, Prev: prev, State: st.State, Result: r.res, At: r.at, Alert: alert}
	e.mu.Unlock()

	if err := e.store.InsertCheck(ctx, store.Check{
		MonitorID:  r.monitorID,
		At:         r.at,
		OK:         r.res.OK,
		LatencyMS:  r.res.Latency.Milliseconds(),
		StatusCode: r.res.StatusCode,
		Error:      r.res.Error,
	}); err != nil {
		e.log.Error("write check", "monitor", r.monitorID, "err", err)
	}
	switch alert {
	case AlertDown:
		if _, err := e.store.OpenIncident(ctx, r.monitorID, r.at, r.res.Error); err != nil {
			e.log.Error("open incident", "monitor", r.monitorID, "err", err)
		}
	case AlertUp:
		if err := e.store.CloseIncident(ctx, r.monitorID, r.at); err != nil {
			e.log.Error("close incident", "monitor", r.monitorID, "err", err)
		}
	}
	if alert != AlertNone {
		e.log.Info("state change", "monitor", r.monitorID, "from", prev, "to", ev.State, "error", r.res.Error)
		go e.notifier.Notify(context.Background(), ev)
	}
	e.hub.Publish(ev)
}
