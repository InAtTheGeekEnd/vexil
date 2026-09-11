package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

const (
	sessionCookie   = "session"
	sessionLifetime = 30 * 24 * time.Hour
)

// newSessionToken returns a random URL-safe token and its SHA-256 hex hash.
func newSessionToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// startSession creates a session row and sets the cookie.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request) error {
	token, hash, err := newSessionToken()
	if err != nil {
		return err
	}
	now := time.Now()
	if err := s.store.CreateSession(r.Context(), hash, now, now.Add(sessionLifetime)); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionLifetime / time.Second),
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// endSession deletes the session row, if any, and clears the cookie.
func (s *Server) endSession(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := s.store.DeleteSession(r.Context(), hashToken(c.Value)); err != nil {
			return err
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// loggedIn reports whether the request carries a valid session cookie.
func (s *Server) loggedIn(r *http.Request) (bool, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false, nil
	}
	return s.store.SessionValid(r.Context(), hashToken(c.Value), time.Now())
}

// isHTTPS reports whether the client reached the server over HTTPS. The
// X-Forwarded-Proto header counts only when the request comes from a
// reverse proxy on a loopback or private address, so a client on the
// internet cannot claim HTTPS.
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return trustedProxy(r.RemoteAddr) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// trustedProxy reports whether a remote address is a loopback or private
// IP address.
func trustedProxy(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsPrivate()
}
