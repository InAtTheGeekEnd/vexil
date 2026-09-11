package web

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"

	"github.com/InAtTheGeekEnd/vexil/internal/brand"
	assets "github.com/InAtTheGeekEnd/vexil/web"
)

// pageData is the data every template receives.
type pageData struct {
	Brand   brand.Brand
	Error   string
	Title   string
	Message string
	// Content carries page-specific data.
	Content any
}

// parseTemplates parses each page together with the layout.
func parseTemplates() (map[string]*template.Template, error) {
	pages, err := fs.Glob(assets.Templates, "templates/*.html")
	if err != nil {
		return nil, err
	}
	out := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		if page == "templates/layout.html" {
			continue
		}
		t, err := template.ParseFS(assets.Templates, "templates/layout.html", page)
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
		data.Brand = s.brand
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
