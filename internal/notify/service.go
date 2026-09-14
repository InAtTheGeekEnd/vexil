package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/engine"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// retryDelays are the waits between attempts (SPEC.md section 7.4).
var retryDelays = []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute}

// errDropped ends the retries of an alert that is no longer true.
var errDropped = errors.New("alert dropped")

// sendTimeout limits one delivery attempt.
const sendTimeout = 15 * time.Second

// Options configures a Service.
type Options struct {
	Log     *slog.Logger // nil means slog.Default()
	Brand   string       // product name when none is saved in the settings
	BaseURL string       // public URL for links, "" means no links
	Client  *http.Client // nil means a client with a timeout
}

// Service turns engine alerts into messages and delivers them to every
// enabled channel. It implements engine.Notifier.
type Service struct {
	store   *store.Store
	log     *slog.Logger
	client  *http.Client
	brand   string
	baseURL string
	sleep   func(context.Context, time.Duration) bool
	wg      sync.WaitGroup
}

// NewService builds a Service.
func NewService(st *store.Store, opts Options) *Service {
	s := &Service{store: st, log: opts.Log, client: opts.Client, brand: opts.Brand, baseURL: opts.BaseURL, sleep: sleep}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.client == nil {
		s.client = &http.Client{Timeout: sendTimeout}
	}
	return s
}

func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// Notify builds the message for an alert and delivers it to every enabled
// channel, each in its own goroutine.
func (s *Service) Notify(ctx context.Context, ev engine.Event) {
	if ev.Alert == engine.AlertNone {
		return
	}
	m, err := s.message(ctx, ev)
	if err != nil {
		s.log.Error("build alert", "monitor", ev.MonitorID, "err", err)
		return
	}
	channels, err := s.store.EnabledChannels(ctx)
	if err != nil {
		s.log.Error("load channels", "err", err)
		return
	}
	for _, c := range channels {
		s.wg.Add(1)
		go func(c store.Channel) {
			defer s.wg.Done()
			s.deliver(context.Background(), c, m)
		}(c)
	}
}

// Wait blocks until every delivery in progress has ended. Tests use it.
func (s *Service) Wait() { s.wg.Wait() }

// brandName returns the product name from the settings, or the default
// from Options when none is saved.
func (s *Service) brandName(ctx context.Context) string {
	name, err := s.store.GetSetting(ctx, store.SettingBrandName)
	if err != nil || strings.TrimSpace(name) == "" {
		return s.brand
	}
	return name
}

// message fills a Message from an event.
func (s *Service) message(ctx context.Context, ev engine.Event) (Message, error) {
	mon, err := s.store.Monitor(ctx, ev.MonitorID)
	if err != nil {
		return Message{}, err
	}
	m := Message{Monitor: mon, At: ev.At, Brand: s.brandName(ctx)}
	if s.baseURL != "" {
		m.URL = fmt.Sprintf("%s/monitors/%d", s.baseURL, mon.ID)
	}
	switch ev.Alert {
	case engine.AlertDown:
		m.Kind, m.Reason = KindDown, ev.Result.Error
		// The engine opens the incident before it sends the event.
		incidents, err := s.store.Incidents(ctx, mon.ID, 1)
		if err != nil {
			return Message{}, err
		}
		if len(incidents) == 1 {
			m.Started = incidents[0].StartedAt
		}
	case engine.AlertUp:
		m.Kind = KindUp
		incidents, err := s.store.Incidents(ctx, mon.ID, 1)
		if err != nil {
			return Message{}, err
		}
		if len(incidents) == 1 && !incidents[0].Open() {
			m.Started = incidents[0].StartedAt
			m.DownFor = incidents[0].EndedAt.Sub(incidents[0].StartedAt)
		}
	case engine.AlertCert:
		m.Kind, m.CertExpiry = KindCert, ev.Result.CertExpiry
	default:
		return Message{}, fmt.Errorf("unknown alert %d", ev.Alert)
	}
	return m, nil
}

// deliver sends m to one channel with retries. The last failure is logged
// and stored on the channel; a success clears it. On a channel that can
// cancel, an UP alert also stops the repeats of the DOWN alert.
func (s *Service) deliver(ctx context.Context, c store.Channel, m Message) {
	sender, err := New(c, s.client)
	if err != nil {
		s.fail(ctx, c, err.Error())
		return
	}
	cn, cancels := sender.(canceler)
	if cancels && m.Kind == KindUp {
		// The cancel runs on its own, so its retries do not hold back the
		// UP alert.
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.cancel(ctx, c, cn, m)
		}()
	}
	attempts := 0
	failed, err := s.withRetries(ctx, c, "alert delivery", func(ctx context.Context) error {
		attempts++
		// A DOWN alert that waits for a retry is dropped once its incident
		// has closed: the UP alert can be out already, and a late DOWN alert
		// would say the monitor is down while it is up. An attempt that is on
		// its way when the incident closes still counts, and the cancel below
		// stops its repeats.
		if attempts > 1 && m.Kind == KindDown && s.recovered(ctx, m) {
			return errDropped
		}
		return sender.Send(ctx, m)
	})
	if errors.Is(err, errDropped) {
		s.log.Info("DOWN alert dropped: the monitor recovered before a retry", "channel", c.ID, "name", c.Name, "monitor", m.Monitor.ID)
		return
	}
	if err != nil {
		if ctx.Err() == nil {
			s.fail(ctx, c, err.Error())
		}
		return
	}
	if c.LastError != "" || failed > 0 {
		if err := s.store.SetChannelError(ctx, c.ID, "", time.Time{}); err != nil {
			s.log.Error("clear channel error", "channel", c.ID, "err", err)
		}
	}
	if cancels && m.Kind == KindDown && s.recovered(ctx, m) {
		// The incident of this alert closed while the alert was on its way,
		// so the cancel of the UP alert can have run too early.
		s.cancel(ctx, c, cn, m)
	}
}

// withRetries calls send until it works, with the waits from SPEC.md
// section 7.4. It returns the number of failed attempts and the last error
// with secrets redacted, or nil on success. It returns ctx.Err() when ctx
// ends during a wait.
func (s *Service) withRetries(ctx context.Context, c store.Channel, what string, send func(context.Context) error) (int, error) {
	for attempt := 0; ; attempt++ {
		actx, cancel := context.WithTimeout(ctx, sendTimeout)
		err := send(actx)
		cancel()
		if err == nil {
			return attempt, nil
		}
		if errors.Is(err, errDropped) {
			return attempt, err
		}
		last := Redact(err.Error(), Secrets(c))
		if attempt >= len(retryDelays) {
			return attempt + 1, errors.New(last)
		}
		s.log.Warn(what+" failed, will retry", "channel", c.ID, "name", c.Name, "attempt", attempt+1, "err", last)
		if !s.sleep(ctx, retryDelays[attempt]) {
			return attempt + 1, ctx.Err()
		}
	}
}

// cancel stops the repeats of the DOWN alert for the incident of m. A
// failure is only logged. The channel error shows alert deliveries only.
func (s *Service) cancel(ctx context.Context, c store.Channel, cn canceler, m Message) {
	_, err := s.withRetries(ctx, c, "alert cancel", func(ctx context.Context) error { return cn.Cancel(ctx, m) })
	if err != nil && ctx.Err() == nil {
		s.log.Error("alert cancel failed", "channel", c.ID, "name", c.Name, "type", c.Type, "err", err.Error())
	}
}

// recovered reports whether the incident of a DOWN message has ended. It
// reads the incident that the message belongs to, not the newest one: the
// monitor can be DOWN again in a new incident.
func (s *Service) recovered(ctx context.Context, m Message) bool {
	if m.Started.IsZero() {
		return false
	}
	inc, err := s.store.IncidentStartedAt(ctx, m.Monitor.ID, m.Started)
	return err == nil && !inc.Open()
}

func (s *Service) fail(ctx context.Context, c store.Channel, msg string) {
	s.log.Error("alert delivery failed", "channel", c.ID, "name", c.Name, "type", c.Type, "err", msg)
	if err := s.store.SetChannelError(ctx, c.ID, msg, time.Now()); err != nil {
		s.log.Error("record channel error", "channel", c.ID, "err", err)
	}
}

// Test sends one test message to a channel at once, without retries. The
// error it returns is safe to show: secrets are redacted.
func (s *Service) Test(ctx context.Context, c store.Channel) error {
	sender, err := New(c, s.client)
	if err != nil {
		return err
	}
	name := s.brandName(ctx)
	m := Message{Kind: KindTest, Monitor: store.Monitor{Name: name}, At: time.Now(), Brand: name}
	if s.baseURL != "" {
		m.URL = s.baseURL + "/notifications"
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if err := sender.Send(ctx, m); err != nil {
		return errors.New(Redact(err.Error(), Secrets(c)))
	}
	return nil
}
