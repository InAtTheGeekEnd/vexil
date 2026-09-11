package web

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

// gzipPool reuses writers; a new gzip.Writer allocates about 800 KB.
var gzipPool = sync.Pool{New: func() any { return gzip.NewWriter(io.Discard) }}

// compress gzips text responses for clients that accept it. The event
// stream is left alone: it must flush every event at once.
func compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/events" || !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

// gzipWriter decides on the first write, from the Content-Type, whether
// to compress.
type gzipWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	decided bool
}

func compressible(contentType string) bool {
	for _, t := range []string{"text/", "application/json", "image/svg+xml"} {
		if strings.HasPrefix(contentType, t) {
			return true
		}
	}
	return false
}

func (w *gzipWriter) WriteHeader(code int) {
	if !w.decided {
		w.decided = true
		h := w.Header()
		if code < 300 && compressible(h.Get("Content-Type")) && h.Get("Content-Encoding") == "" {
			h.Set("Content-Encoding", "gzip")
			h.Add("Vary", "Accept-Encoding")
			h.Del("Content-Length")
			w.gz = gzipPool.Get().(*gzip.Writer)
			w.gz.Reset(w.ResponseWriter)
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *gzipWriter) Write(b []byte) (int, error) {
	if !w.decided {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", http.DetectContentType(b))
		}
		w.WriteHeader(http.StatusOK)
	}
	if w.gz != nil {
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (w *gzipWriter) close() {
	if w.gz != nil {
		_ = w.gz.Close()
		gzipPool.Put(w.gz)
		w.gz = nil
	}
}

// Unwrap lets http.ResponseController reach the real writer.
func (w *gzipWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
