package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/InAtTheGeekEnd/vexil/internal/notify"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// --- Notifications page ---

type channelsContent struct {
	Rows []channelRow
}

type channelRow struct {
	ID        int64
	Type      string
	TypeLabel string
	Name      string
	Summary   string
	Enabled   bool
	LastError string // "Failed 12 m ago: HTTP 403", "" when fine
}

func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	s.renderChannels(w, r, http.StatusOK, "", "")
}

// renderChannels shows the list with an optional notice or error line.
func (s *Server) renderChannels(w http.ResponseWriter, r *http.Request, status int, notice, errText string) {
	channels, err := s.store.Channels(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	now := time.Now()
	var c channelsContent
	for _, ch := range channels {
		row := channelRow{
			ID:        ch.ID,
			Type:      ch.Type,
			TypeLabel: notify.TypeLabel(ch.Type),
			Name:      ch.Name,
			Summary:   notify.Summary(ch),
			Enabled:   ch.Enabled,
		}
		if ch.LastError != "" {
			row.LastError = fmt.Sprintf("Failed %s: %s", ago(now.Sub(ch.LastErrorAt)), ch.LastError)
		}
		c.Rows = append(c.Rows, row)
	}
	s.render(w, status, "channels.html", pageData{Content: c, Nav: "notifications", Notice: notice, Error: errText})
}

// loadChannel reads the {id} path value. It renders 404 and returns false
// when the channel does not exist.
func (s *Server) loadChannel(w http.ResponseWriter, r *http.Request) (store.Channel, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderError(w, http.StatusNotFound)
		return store.Channel{}, false
	}
	c, err := s.store.Channel(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.renderError(w, http.StatusNotFound)
		return store.Channel{}, false
	}
	if err != nil {
		s.serverError(w, err)
		return store.Channel{}, false
	}
	return c, true
}

func (s *Server) handleChannelToggle(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadChannel(w, r)
	if !ok {
		return
	}
	if err := s.store.SetChannelEnabled(r.Context(), c.ID, !c.Enabled); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/notifications", http.StatusSeeOther)
}

func (s *Server) handleChannelDelete(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadChannel(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteChannel(r.Context(), c.ID); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/notifications", http.StatusSeeOther)
}

// handleChannelTest sends one test message and shows the result on the
// list page.
func (s *Server) handleChannelTest(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadChannel(w, r)
	if !ok {
		return
	}
	if s.notifier == nil {
		s.renderChannels(w, r, http.StatusServiceUnavailable, "", "Notifications are not running.")
		return
	}
	if err := s.notifier.Test(r.Context(), c); err != nil {
		s.renderChannels(w, r, http.StatusOK, "", fmt.Sprintf("The test to %s failed: %s. Check the settings and try again.", c.Name, err))
		return
	}
	s.renderChannels(w, r, http.StatusOK, fmt.Sprintf("Test sent to %s. Check that it arrived.", c.Name), "")
}

// --- Add and edit form ---

// channelForm is the template content of channel_form.html.
type channelForm struct {
	ID      int64
	Type    string
	Name    string
	Values  map[string]string // field values to show; secrets are empty
	HasKeys map[string]bool   // secret fields that have a saved value
	Errors  map[string]string
	Types   []channelType
	Fields  map[string][]notify.Field
}

type channelType struct {
	Type, Label, Description string
}

func newChannelForm() channelForm {
	f := channelForm{
		Type:    store.ChannelNtfy,
		Values:  map[string]string{},
		HasKeys: map[string]bool{},
		Errors:  map[string]string{},
		Fields:  map[string][]notify.Field{},
	}
	for _, t := range notify.Types {
		f.Types = append(f.Types, channelType{t.Type, t.Label, t.Description})
		f.Fields[t.Type] = notify.Fields(t.Type)
	}
	return f
}

// formFromChannel fills the form from a saved channel. Secret values stay
// out of the page.
func formFromChannel(c store.Channel) channelForm {
	f := newChannelForm()
	f.ID, f.Type, f.Name = c.ID, c.Type, c.Name
	for _, fd := range notify.Fields(c.Type) {
		if fd.Secret {
			f.HasKeys[fd.Key] = c.Config[fd.Key] != ""
			continue
		}
		f.Values[fd.Key] = c.Config[fd.Key]
	}
	return f
}

// parseChannelForm reads the posted fields for the given type. The inputs
// are named "{type}_{key}": every type has its own inputs in one form, so
// plain keys like "url" would collide. old holds the saved config on edit:
// an empty secret keeps the old value.
func parseChannelForm(r *http.Request, typ string, old map[string]string) (channelForm, store.Channel) {
	f := newChannelForm()
	f.Type = typ
	f.Name = strings.TrimSpace(r.FormValue("name"))
	c := store.Channel{Type: typ, Config: map[string]string{}}
	for _, fd := range notify.Fields(typ) {
		v := strings.TrimSpace(r.FormValue(typ + "_" + fd.Key))
		if fd.Secret {
			if v == "" {
				v = old[fd.Key]
			}
			f.HasKeys[fd.Key] = v != ""
		} else {
			f.Values[fd.Key] = v
		}
		c.Config[fd.Key] = v
	}
	if f.Name == "" {
		f.Name = notify.TypeLabel(typ)
	}
	c.Name = f.Name
	f.Errors = notify.Validate(c)
	if utf8.RuneCountInString(f.Name) > maxNameLen {
		f.Errors["name"] = nameTooLong
	}
	return f, c
}

func (s *Server) handleChannelNewForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "channel_form.html", pageData{Content: newChannelForm(), Nav: "notifications"})
}

func (s *Server) handleChannelCreate(w http.ResponseWriter, r *http.Request) {
	f, c := parseChannelForm(r, strings.TrimSpace(r.FormValue("type")), nil)
	if len(f.Errors) > 0 {
		s.render(w, http.StatusBadRequest, "channel_form.html", pageData{Content: f, Nav: "notifications"})
		return
	}
	c.Enabled = true
	if err := s.store.CreateChannel(r.Context(), &c); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/notifications", http.StatusSeeOther)
}

func (s *Server) handleChannelEditForm(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadChannel(w, r)
	if !ok {
		return
	}
	s.render(w, http.StatusOK, "channel_form.html", pageData{Content: formFromChannel(c), Nav: "notifications"})
}

func (s *Server) handleChannelUpdate(w http.ResponseWriter, r *http.Request) {
	old, ok := s.loadChannel(w, r)
	if !ok {
		return
	}
	f, c := parseChannelForm(r, old.Type, old.Config) // the type is fixed after creation
	f.ID = old.ID
	if len(f.Errors) > 0 {
		s.render(w, http.StatusBadRequest, "channel_form.html", pageData{Content: f, Nav: "notifications"})
		return
	}
	c.ID = old.ID
	if err := s.store.UpdateChannel(r.Context(), c); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/notifications", http.StatusSeeOther)
}
