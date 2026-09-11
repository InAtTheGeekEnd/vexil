package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"sort"

	"github.com/InAtTheGeekEnd/vexil/internal/brand"
	assets "github.com/InAtTheGeekEnd/vexil/web"
)

// pageData is the data every template receives.
type pageData struct {
	Brand   brand.Brand
	Logo    string // URL of the uploaded logo, "" for the built-in one
	Theme   string // URL of the accent style sheet
	Error   string
	Notice  string // a green line at the top of the page
	Title   string
	Message string
	Nav     string // the active nav item: "dashboard", "notifications" or "settings"
	// Live makes the page open the SSE stream. Down is the number of
	// monitors that are down, shown in the tab title.
	Live bool
	Down int
	// Content carries page-specific data.
	Content any
}

// assetVersion is a short hash of every embedded static file. It goes on
// the static URLs as ?v=, so a new build never loads a cached old file.
var assetVersion = hashStatic()

func hashStatic() string {
	var names []string
	_ = fs.WalkDir(assets.Static, "static", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			names = append(names, p)
		}
		return nil
	})
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		b, err := fs.ReadFile(assets.Static, name)
		if err != nil {
			panic("embedded static file unreadable: " + err.Error())
		}
		h.Write([]byte(name))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// assetURL returns the versioned URL of a file under /static/.
func assetURL(name string) string {
	return "/static/" + name + "?v=" + assetVersion
}

// parseTemplates parses each page together with the layout.
func parseTemplates() (map[string]*template.Template, error) {
	pages, err := fs.Glob(assets.Templates, "templates/*.html")
	if err != nil {
		return nil, err
	}
	funcs := template.FuncMap{"asset": assetURL}
	out := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		if page == "templates/layout.html" {
			continue
		}
		t, err := template.New("").Funcs(funcs).ParseFS(assets.Templates, "templates/layout.html", page)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", page, err)
		}
		out[page[len("templates/"):]] = t
	}
	return out, nil
}

// render writes a page. It buffers the output so a template error becomes a
// clean 500 instead of a half page.
func (s *Server) render(w http.ResponseWriter, status int, name string, data pageData) {
	t, ok := s.tmpl[name]
	if !ok {
		s.log.Error("missing template", "name", name)
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	if data.Brand.Name == "" {
		b := s.currentBrand()
		data.Brand, data.Logo, data.Theme = b.Brand, b.LogoURL, b.ThemeURL
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		s.log.Error("render template", "name", name, "err", err)
		http.Error(w, "could not render the page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (s *Server) renderError(w http.ResponseWriter, status int) {
	d := pageData{Title: "Something went wrong", Message: "The server hit an error. Try again. If it continues, check the server logs."}
	if status == http.StatusNotFound {
		d.Title = "Page not found"
		d.Message = "This page does not exist. Check the address or go back to the dashboard."
	}
	s.render(w, status, "error.html", d)
}
