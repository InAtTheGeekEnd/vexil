// Package web serves the HTTP UI.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/brand"
	"github.com/InAtTheGeekEnd/vexil/internal/engine"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
	assets "github.com/InAtTheGeekEnd/vexil/web"
)

// Options configures a Server.
type Options struct {
	// Brand is the product identity. Zero value means brand.Default.
	Brand brand.Brand
	// Log receives access and error logs. Nil means slog.Default().
	Log *slog.Logger
	// Engine is the running check engine. Nil means no engine, which makes
	// /readyz fail and /push return 404.
	Engine *engine.Engine
}

// Server holds the handlers and their dependencies.
type Server struct {
	store      *store.Store
	brand      brand.Brand
	log        *slog.Logger
	tmpl       map[string]*template.Template
	engine     *engine.Engine
	loginLimit *rateLimiter
	handler    http.Handler
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
		brand:      opts.Brand,
		log:        opts.Log,
		tmpl:       tmpl,
		engine:     opts.Engine,
		loginLimit: newRateLimiter(5, time.Minute),
	}
	if s.brand.Name == "" {
		s.brand = brand.Default
	}
	if s.log == nil {
		s.log = slog.Default()
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

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.renderError(w, http.StatusNotFound)
	})

	csrf := http.NewCrossOriginProtection()
	var h http.Handler = csrf.Handler(mux)
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

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
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
	if err := s.store.SetPasswordHash(r.Context(), hash); err != nil {
		s.serverError(w, err)
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

// --- Dashboard ---

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "dashboard.html", pageData{})
}

// --- Helpers ---

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
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
