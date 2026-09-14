// Package web serves the HTTP UI.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/brand"
	"github.com/InAtTheGeekEnd/vexil/internal/engine"
	"github.com/InAtTheGeekEnd/vexil/internal/notify"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
	assets "github.com/InAtTheGeekEnd/vexil/web"
)

// Options configures a Server.
type Options struct {
	// Brand is the product identity used until the user saves one in
	// Settings. Zero value means brand.Default.
	Brand brand.Brand
	// Log receives access and error logs. Nil means slog.Default().
	Log *slog.Logger
	// Engine is the running check engine. Nil means no engine, which makes
	// /readyz fail and /push return 404.
	Engine *engine.Engine
	// BaseURL is the public URL of the server without a trailing slash. It
	// builds push URLs. Empty means use the host of the request.
	BaseURL string
	// Notifier sends test messages. Nil disables the Send test button.
	Notifier *notify.Service
}

// Server holds the handlers and their dependencies.
type Server struct {
	store      *store.Store
	defaults   brand.Brand // the brand before any setting is saved
	brand      atomic.Pointer[brandState]
	log        *slog.Logger
	tmpl       map[string]*template.Template
	engine     *engine.Engine
	notifier   *notify.Service
	baseURL    string
	loginLimit *rateLimiter
	handler    http.Handler
	closing    chan struct{} // closed by CloseEvents to end every SSE stream
	closeOnce  sync.Once
}

// New builds a Server. It panics only through parseTemplates when the
// embedded templates are broken, which is a build error.
func New(st *store.Store, opts Options) (*Server, error) {
	tmpl, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	s := &Server{
		store:      st,
		defaults:   opts.Brand,
		log:        opts.Log,
		tmpl:       tmpl,
		engine:     opts.Engine,
		notifier:   opts.Notifier,
		baseURL:    strings.TrimRight(opts.BaseURL, "/"),
		loginLimit: newRateLimiter(5, time.Minute),
		closing:    make(chan struct{}),
	}
	if s.defaults.Name == "" {
		s.defaults = brand.Default
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if err := s.loadBrand(context.Background()); err != nil {
		return nil, err
	}
	s.handler = s.routes()
	return s, nil
}

// ServeHTTP makes Server an http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /push/{token}", s.handlePush)
	mux.HandleFunc("POST /push/{token}", s.handlePush)
	mux.HandleFunc("GET /brand/logo", s.handleLogo)
	mux.HandleFunc("GET /brand/theme.css", s.handleTheme)
	mux.HandleFunc("GET /brand/apple-touch-icon.png", s.handleTouchIcon)
	mux.HandleFunc("GET /brand/favicon-up.svg", s.handleFaviconUp)
	mux.HandleFunc("GET /brand/favicon-down.svg", s.handleFaviconDown)
	mux.HandleFunc("GET /status", s.handleStatus)
	mux.HandleFunc("GET /badge/{file}", s.handleBadge)

	static, err := fs.Sub(assets.Static, "static")
	if err != nil {
		panic("embedded static folder missing: " + err.Error())
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServerFS(static))))

	mux.HandleFunc("GET /setup", s.handleSetupForm)
	mux.HandleFunc("POST /setup", s.handleSetup)
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)

	mux.Handle("GET /{$}", s.requireAdmin(http.HandlerFunc(s.handleDashboard)))
	mux.Handle("GET /styleguide", s.requireAdmin(http.HandlerFunc(s.handleStyleguide)))
	mux.Handle("GET /events", s.requireSession(http.HandlerFunc(s.handleEvents)))
	admin := map[string]http.HandlerFunc{
		"GET /monitors/new":               s.handleMonitorNewForm,
		"POST /monitors/new":              s.handleMonitorCreate,
		"POST /monitors/reorder":          s.handleReorder,
		"GET /monitors/{id}":              s.handleMonitor,
		"GET /monitors/{id}/edit":         s.handleMonitorEditForm,
		"POST /monitors/{id}/edit":        s.handleMonitorUpdate,
		"POST /monitors/{id}/pause":       s.handleMonitorPause,
		"POST /monitors/{id}/resume":      s.handleMonitorResume,
		"POST /monitors/{id}/delete":      s.handleMonitorDelete,
		"GET /groups":                     s.handleGroups,
		"GET /groups/new":                 s.handleGroupNewForm,
		"POST /groups/new":                s.handleGroupCreate,
		"GET /groups/{id}/edit":           s.handleGroupEditForm,
		"POST /groups/{id}/edit":          s.handleGroupUpdate,
		"POST /groups/{id}/delete":        s.handleGroupDelete,
		"GET /notifications":              s.handleChannels,
		"GET /notifications/new":          s.handleChannelNewForm,
		"POST /notifications/new":         s.handleChannelCreate,
		"GET /notifications/{id}/edit":    s.handleChannelEditForm,
		"POST /notifications/{id}/edit":   s.handleChannelUpdate,
		"POST /notifications/{id}/toggle": s.handleChannelToggle,
		"POST /notifications/{id}/test":   s.handleChannelTest,
		"POST /notifications/{id}/delete": s.handleChannelDelete,
		"GET /settings":                   s.handleSettings,
		"POST /settings":                  s.handleSettingsSave,
		"POST /settings/logo/delete":      s.handleLogoDelete,
		"POST /settings/password":         s.handlePasswordChange,
	}
	for pattern, h := range admin {
		mux.Handle(pattern, s.requireAdmin(h))
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.renderError(w, http.StatusNotFound)
	})

	csrf := http.NewCrossOriginProtection()
	var h http.Handler = csrf.Handler(mux)
	h = compress(h)
	h = s.accessLog(h)
	h = securityHeaders(h)
	return h
}

// requireAdmin sends first-run visitors to /setup and anonymous visitors to
// /login.
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		has, err := s.store.HasPassword(r.Context())
		if err != nil {
			s.serverError(w, err)
			return
		}
		if !has {
			http.Redirect(w, r, "/setup", http.StatusFound)
			return
		}
		ok, err := s.loggedIn(r)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if !ok {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireSession is requireAdmin for the event stream. An EventSource cannot
// show a login page, so a request without a valid session gets 401 and the
// page marks its data as old.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, err := s.loggedIn(r)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if !ok {
			http.Error(w, "log in first", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	s.log.Error("request failed", "err", err)
	s.renderError(w, http.StatusInternalServerError)
}

// --- Health ---

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Millisecond)
	defer cancel()

	body := map[string]string{"status": "ok", "database": "ok", "engine": "ok"}
	if err := s.store.Ping(ctx); err != nil {
		body["status"] = "fail"
		body["database"] = shortReason(err)
	}
	if err := s.engineReady(); err != nil {
		body["status"] = "fail"
		body["engine"] = shortReason(err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if body["status"] != "ok" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) engineReady() error {
	if s.engine == nil {
		return errors.New("not started")
	}
	return s.engine.Ready()
}

// handlePush records a heartbeat for a push monitor. It needs no login: the
// token is the secret.
func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.engine == nil {
		http.NotFound(w, r)
		return
	}
	err := s.engine.Push(r.Context(), r.PathValue("token"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.log.Error("push", "err", err)
		http.Error(w, "could not record the push", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

func shortReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	msg := err.Error()
	if len(msg) > 60 {
		msg = msg[:60]
	}
	return msg
}

// --- Setup ---

func (s *Server) handleSetupForm(w http.ResponseWriter, r *http.Request) {
	has, err := s.store.HasPassword(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	if has {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	s.render(w, http.StatusOK, "setup.html", pageData{})
}

// maxAuthForm is the body limit of the login and setup forms. They carry
// one or two passwords.
const maxAuthForm = 8 << 10

// parseAuthForm reads the small URL-encoded body of a form that needs no
// login. It refuses any other content type, multipart included, before it
// reads the body, so an anonymous client cannot stream an upload to a temp
// file.
func parseAuthForm(w http.ResponseWriter, r *http.Request) bool {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/x-www-form-urlencoded" {
		http.Error(w, "send the form as application/x-www-form-urlencoded", http.StatusUnsupportedMediaType)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthForm)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "the form is too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "the form could not be read", http.StatusBadRequest)
		}
		return false
	}
	return true
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if !parseAuthForm(w, r) {
		return
	}
	has, err := s.store.HasPassword(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	if has {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	password := r.FormValue("password")
	if err := ValidateNewPassword(password, r.FormValue("confirm")); err != nil {
		s.render(w, http.StatusBadRequest, "setup.html", pageData{Error: capitalize(err.Error()) + "."})
		return
	}
	hash, err := HashPassword(password)
	if err != nil {
		s.serverError(w, err)
		return
	}
	set, err := s.store.SetFirstPasswordHash(r.Context(), hash)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !set {
		// Another setup request set the password first.
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if err := s.startSession(w, r); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// --- Login and logout ---

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	has, err := s.store.HasPassword(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !has {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}
	if ok, _ := s.loggedIn(r); ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	s.render(w, http.StatusOK, "login.html", pageData{})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !parseAuthForm(w, r) {
		return
	}
	hash, err := s.store.PasswordHash(r.Context())
	if errors.Is(err, store.ErrNotFound) {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !s.loginLimit.allow(clientIP(r), time.Now()) {
		s.render(w, http.StatusTooManyRequests, "login.html",
			pageData{Error: "Too many attempts. Wait one minute and try again."})
		return
	}
	if !checkPassword(hash, r.FormValue("password")) {
		s.render(w, http.StatusUnauthorized, "login.html",
			pageData{Error: "Wrong password. Try again."})
		return
	}
	if err := s.startSession(w, r); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.endSession(w, r); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// --- Helpers ---

// clientIP returns the address that the login limit counts. Behind a
// reverse proxy on a loopback or private address, it is the rightmost
// address in X-Forwarded-For, which the proxy appends. A client on the
// internet connects from a public address, so an X-Forwarded-For header
// that it sends itself does not count.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	forwarded := r.Header.Values("X-Forwarded-For")
	if len(forwarded) == 0 || !trustedProxy(r.RemoteAddr) {
		return host
	}
	last := forwarded[len(forwarded)-1]
	if i := strings.LastIndexByte(last, ','); i >= 0 {
		last = last[i+1:]
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(last))
	if err != nil {
		return host
	}
	return ip.Unmap().String()
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	if s[0] >= 'a' && s[0] <= 'z' {
		return string(s[0]-'a'+'A') + s[1:]
	}
	return s
}
