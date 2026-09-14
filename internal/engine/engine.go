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

// ShutdownBudget is the most that a whole shutdown may take, so it ends
// before Docker stops waiting after 10 seconds (SPEC.md section 6.3).
const ShutdownBudget = 8 * time.Second

const (
	maxConcurrent  = 50
	certWarnBefore = 14 * 24 * time.Hour

	// Timing from SPEC.md sections 4.4, 5.5 and 6.3. Tests shorten these
	// through the Engine fields.
	defaultRetryDelay   = 30 * time.Second // next check after a failed check
	defaultMaxOffset    = 60 * time.Second // upper limit of the random start offset
	defaultPushMinExtra = 30 * time.Second // least extra time a push may be late
)

// Notifier receives alerts: DOWN, UP and certificate warnings.
type Notifier interface {
	Notify(ctx context.Context, ev Event)
}

// deliveryWaiter is a Notifier that can wait for its deliveries in progress.
// Stop gives it what is left of the shutdown budget.
type deliveryWaiter interface {
	WaitUntil(deadline time.Time) bool
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

	// newChecker and the timing fields are replaced in tests.
	newChecker   func(store.Monitor) (check.Checker, error)
	interval     func(store.Monitor) time.Duration
	retryDelay   time.Duration
	maxOffset    time.Duration
	pushMinExtra time.Duration
	stopWait     time.Duration // the budget of Stop

	sem      chan struct{}
	results  chan result
	stopping chan struct{} // closed by Stop: runners start no new check
	stopCh   chan struct{}
	done     chan struct{}

	reloadMu sync.Mutex     // one Reload at a time
	writeMu  sync.Mutex     // the writer and DeleteMonitor take turns
	notifyWG sync.WaitGroup // notifier calls in progress

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
		interval:     store.Monitor.Interval,
		retryDelay:   defaultRetryDelay,
		maxOffset:    defaultMaxOffset,
		pushMinExtra: defaultPushMinExtra,
		stopWait:     ShutdownBudget,
		sem:          make(chan struct{}, maxConcurrent),
		results:      make(chan result, 256),
		stopping:     make(chan struct{}),
		stopCh:       make(chan struct{}),
		done:         make(chan struct{}),
		runners:      make(map[int64]*runner),
		status:       make(map[int64]*Status),
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
			e.startRunnerLocked(m, false)
		}
	}
	e.started = true
	go e.writer()
	e.log.Info("engine started", "monitors", len(monitors))
	return nil
}

// Stop stops the engine within its budget, ShutdownBudget unless a test sets
// another. See StopBy.
func (e *Engine) Stop() {
	e.StopBy(time.Now().Add(e.stopWait))
}

// StopBy stops the engine by the deadline (SPEC.md section 6.3). The stages
// share the deadline and run in order, each with what is left of it: stop the
// tickers and wait for the running checks, flush the writer, wait for the
// alert calls, then wait for the deliveries in progress when the notifier can
// wait for them.
func (e *Engine) StopBy(deadline time.Time) {
	e.mu.Lock()
	if !e.started || e.stopped {
		e.mu.Unlock()
		return
	}
	e.stopped = true
	// Stop the schedules, but let a running check end and store its result
	// (SPEC.md section 6.3). The contexts are cancelled only after the
	// deadline.
	close(e.stopping)
	runners := make([]*runner, 0, len(e.runners))
	for _, r := range e.runners {
		runners = append(runners, r)
	}
	e.mu.Unlock()

	// Running checks. One timer for all runners: once it fires, Stop waits for
	// none of them. A timer channel delivers once, so a select on it per
	// runner would wait without a limit for every runner after the first late
	// one.
	timeout := time.NewTimer(time.Until(deadline))
	defer timeout.Stop()
wait:
	for _, r := range runners {
		select {
		case <-r.done:
		case <-timeout.C:
			e.log.Warn("engine stop: checks still running after timeout")
			break wait
		}
	}
	e.cancel()
	close(e.stopCh)
	<-e.done
	// Alert calls. The writer has flushed, so no alert is handed over after
	// this.
	if !waitUntil(&e.notifyWG, deadline) {
		e.log.Warn("engine stop: alerts still being handed over at the end of the shutdown budget")
	}
	// Deliveries in progress, so the alerts of the last results go out while
	// the database is still open.
	if d, ok := e.notifier.(deliveryWaiter); ok && !d.WaitUntil(deadline) {
		e.log.Warn("engine stop: alert deliveries still running at the end of the shutdown budget were dropped")
	}
	e.log.Info("engine stopped")
}

// waitUntil waits for wg until the deadline and reports whether wg ended.
func waitUntil(wg *sync.WaitGroup, deadline time.Time) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// Reload restarts the goroutine of one monitor after an edit, pause,
// resume or delete. Call it after the store change.
func (e *Engine) Reload(ctx context.Context, id int64) error {
	// Two reloads of one monitor must not overlap: both would stop the old
	// runner and both would start a new one. The store read is inside the
	// lock, so a reload that read the monitor before a delete cannot start a
	// runner after the reload that saw the delete.
	e.reloadMu.Lock()
	defer e.reloadMu.Unlock()
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
			e.startRunnerLocked(m, prev == Paused)
		}
	}
	if st.State != prev {
		e.hub.Publish(Event{MonitorID: id, Prev: prev, State: st.State, At: time.Now()})
	}
	return nil
}

// DeleteMonitor deletes a monitor with its history and forgets its status.
// It takes turns with the writer: a result that the writer has written goes
// with the delete, and a result that comes later finds no status and is
// dropped. A check that was already running can finish after the delete, so
// the store transaction alone cannot stop that write. Call Reload after it
// to stop the runner.
func (e *Engine) DeleteMonitor(ctx context.Context, id int64) error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	if err := e.store.DeleteMonitor(ctx, id); err != nil {
		return err
	}
	e.mu.Lock()
	prev := Pending
	if st := e.status[id]; st != nil {
		prev = st.State
	}
	delete(e.status, id)
	e.mu.Unlock()
	// Open pages take the monitor off and correct their down count.
	e.hub.Publish(Event{MonitorID: id, Prev: prev, State: Deleted, At: time.Now()})
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
	st := &Status{State: Pending, CertWarned: m.CertWarnedAt}
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
	} else if len(recent) > st.Fails {
		// A success comes before the failures, so the monitor was UP.
		st.State = Up
	}
	// Otherwise the only checks are failures: no check ever succeeded, so
	// the state stays PENDING.
	return st, nil
}

// startRunnerLocked starts the goroutine for m. The caller holds e.mu.
// resumed is true when a paused monitor starts again.
func (e *Engine) startRunnerLocked(m store.Monitor, resumed bool) {
	// A runner in the map is never replaced while it runs. The caller cannot
	// wait for it here, as it can need e.mu to end.
	if old := e.runners[m.ID]; old != nil {
		old.cancel()
	}
	ctx, cancel := context.WithCancel(e.baseCtx)
	r := &runner{cancel: cancel, done: make(chan struct{})}
	e.runners[m.ID] = r
	go e.run(ctx, m, r, resumed)
}

// run is the goroutine of one monitor. Each tick says how long to wait for
// the next one: the interval after a success, retryDelay after a failure.
// The wait counts from the start of the tick so a slow check does not
// shift the schedule.
func (e *Engine) run(ctx context.Context, m store.Monitor, r *runner, resumed bool) {
	defer close(r.done)

	interval := e.interval(m)
	if interval <= 0 {
		interval = time.Minute
	}
	tick := e.tickFunc(m, interval, resumed)
	if tick == nil {
		return
	}

	timer := time.NewTimer(e.firstDelay(m, interval))
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
		case <-ctx.Done():
			return
		case <-e.stopping:
			return
		}
		// The timer and a Stop can be ready at the same moment. A stopping
		// engine starts no new check.
		select {
		case <-e.stopping:
			return
		default:
		}
		started := time.Now()
		next := tick(ctx)
		if ctx.Err() != nil {
			return
		}
		timer.Reset(max(0, time.Until(started.Add(next))))
	}
}

// firstDelay is the wait before the first tick of a runner. It counts from
// the last check in the status, which Start restores from the database, so
// a restart does not reset the schedule. A monitor with no check yet, or
// with a check that is overdue, gets a random start offset.
// A push monitor ticks at once: its tick computes the deadline itself.
func (e *Engine) firstDelay(m store.Monitor, interval time.Duration) time.Duration {
	if m.Type == store.TypePush {
		return 0
	}
	e.mu.Lock()
	st := e.status[m.ID]
	var last time.Time
	var ok bool
	if st != nil {
		last, ok = st.LastAt, st.Last.OK
	}
	e.mu.Unlock()
	if last.IsZero() {
		return e.startOffset(interval)
	}
	wait := interval
	if !ok {
		wait = e.retryDelay
	}
	if d := time.Until(last.Add(wait)); d > 0 {
		return d
	}
	// The check is overdue, as after a downtime longer than the interval.
	// Without the offset, every overdue monitor would check in the same
	// second and stay in step after that.
	return e.startOffset(interval)
}

// startOffset is a random wait of at most maxOffset, or the interval if that
// is shorter (SPEC.md section 6.3).
func (e *Engine) startOffset(interval time.Duration) time.Duration {
	limit := min(e.maxOffset, interval)
	if limit <= 0 {
		return 0
	}
	return rand.N(limit)
}

// pushGrace is how long a push monitor may go without a push: the interval
// plus 25%, with at least pushMinExtra extra (SPEC.md section 5.5).
func (e *Engine) pushGrace(interval time.Duration) time.Duration {
	return interval + max(interval/4, e.pushMinExtra)
}

// tickFunc returns what one tick does for a monitor. The function returns
// the wait until the next tick. resumed is true when a paused monitor starts
// again.
func (e *Engine) tickFunc(m store.Monitor, interval time.Duration, resumed bool) func(context.Context) time.Duration {
	if m.Type == store.TypePush {
		grace := e.pushGrace(interval)
		start := time.Now()
		return func(ctx context.Context) time.Duration {
			now := time.Now()
			e.mu.Lock()
			ref := start
			// The last push counts, also as restored from the database at
			// start. After a resume, only a push after the resume counts: no
			// push was due while the monitor was paused.
			if st := e.status[m.ID]; st != nil && !st.LastPush.IsZero() && (!resumed || st.LastPush.After(start)) {
				ref = st.LastPush
			}
			e.mu.Unlock()
			if wait := ref.Add(grace).Sub(now); wait > 0 {
				return wait
			}
			e.enqueue(result{monitorID: m.ID, res: check.Result{Error: "no push received"}, at: now})
			return e.retryDelay
		}
	}

	checker, err := e.newChecker(m)
	if err != nil {
		e.log.Error("monitor has no checker", "monitor", m.ID, "err", err)
		return nil
	}
	return func(ctx context.Context) time.Duration {
		select {
		case e.sem <- struct{}{}:
		case <-ctx.Done():
			return 0
		case <-e.stopping:
			return 0
		}
		defer func() { <-e.sem }()

		cctx, cancel := context.WithTimeout(ctx, check.Timeout)
		res := checker.Check(cctx)
		cancel()
		if ctx.Err() != nil {
			return 0 // shutting down or reloading: drop the result
		}
		e.enqueue(result{monitorID: m.ID, res: res, at: time.Now()})
		if res.OK {
			return interval
		}
		return e.retryDelay
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
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
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
	certWarn := r.res.OK && certExpiring(r.res.CertExpiry, r.at) && !st.CertWarned.Equal(r.res.CertExpiry)
	if certWarn {
		st.CertWarned = r.res.CertExpiry
	}
	e.mu.Unlock()

	// A monitor that was DOWN can be paused and resumed, which restarts it
	// at PENDING while its incident stays open. That incident is still the
	// same outage: the first success closes it with an UP alert, and new
	// failures open no second incident and send no second DOWN alert.
	switch {
	case alert == AlertNone && ev.State == Up && prev != Up:
		if e.hasOpenIncident(ctx, r.monitorID) {
			alert = AlertUp
		}
	case alert == AlertDown:
		if e.hasOpenIncident(ctx, r.monitorID) {
			alert = AlertNone
		}
	}
	ev.Alert = alert

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
		e.notify(ev)
	}
	if certWarn {
		// Record it first so a restart does not send the warning again.
		if err := e.store.SetCertWarned(ctx, r.monitorID, r.res.CertExpiry); err != nil {
			e.log.Error("record certificate warning", "monitor", r.monitorID, "err", err)
		}
		e.log.Info("certificate expires soon", "monitor", r.monitorID, "expiry", r.res.CertExpiry)
		cert := ev
		cert.Alert = AlertCert
		e.notify(cert)
	}
	e.hub.Publish(ev)
}

// notify hands an alert to the notifier in its own goroutine, so a slow
// channel does not hold the writer. Stop waits for these calls.
func (e *Engine) notify(ev Event) {
	e.notifyWG.Add(1)
	go func() {
		defer e.notifyWG.Done()
		e.notifier.Notify(context.Background(), ev)
	}()
}

// hasOpenIncident reports whether a monitor has an open incident. A read
// error counts as no incident and is logged.
func (e *Engine) hasOpenIncident(ctx context.Context, id int64) bool {
	_, err := e.store.CurrentIncident(ctx, id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		e.log.Error("read open incident", "monitor", id, "err", err)
	}
	return err == nil
}

// certExpiring reports whether a certificate expires within certWarnBefore
// of now. An expired certificate fails the check instead.
func certExpiring(expiry, now time.Time) bool {
	if expiry.IsZero() || !expiry.After(now) {
		return false
	}
	return expiry.Sub(now) <= certWarnBefore
}

// ErrNoChecker is returned by CheckNow for monitors that have no active
// checker: push monitors and paused monitors.
var ErrNoChecker = errors.New("monitor has no active checker")

// CheckNow runs one check for a monitor at once, records the result like a
// scheduled check and returns it. The form handler uses it so the user sees
// a result right after saving.
func (e *Engine) CheckNow(ctx context.Context, id int64) (check.Result, error) {
	m, err := e.store.Monitor(ctx, id)
	if err != nil {
		return check.Result{}, err
	}
	if m.Paused || m.Type == store.TypePush {
		return check.Result{}, ErrNoChecker
	}
	checker, err := e.newChecker(m)
	if err != nil {
		return check.Result{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, check.Timeout)
	defer cancel()
	res := checker.Check(cctx)
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	e.enqueue(result{monitorID: id, res: res, at: time.Now()})
	return res, nil
}
