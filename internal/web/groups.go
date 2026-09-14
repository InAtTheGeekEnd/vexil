package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// --- Groups page ---

type groupsContent struct {
	Rows []groupRow
}

type groupRow struct {
	ID      int64
	Name    string
	Count   string // "3 monitors"
	Confirm string // the question of the delete dialog
}

func (s *Server) handleGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.store.Groups(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	var c groupsContent
	for _, g := range groups {
		c.Rows = append(c.Rows, groupRow{
			ID:      g.ID,
			Name:    g.Name,
			Count:   monitorCount(g.Monitors),
			Confirm: deleteGroupConfirm(g.Name, g.Monitors),
		})
	}
	s.render(w, http.StatusOK, "groups.html", pageData{Content: c, Nav: "groups"})
}

func monitorCount(n int) string {
	switch n {
	case 0:
		return "No monitors"
	case 1:
		return "1 monitor"
	}
	return fmt.Sprintf("%d monitors", n)
}

// deleteGroupConfirm asks before a delete and says how many monitors move
// out of the group.
func deleteGroupConfirm(name string, n int) string {
	switch n {
	case 0:
		return fmt.Sprintf("Delete %s? No monitors are in it.", name)
	case 1:
		return fmt.Sprintf("Delete %s? 1 monitor moves out of the group. It is not deleted.", name)
	}
	return fmt.Sprintf("Delete %s? %d monitors move out of the group. They are not deleted.", name, n)
}

// loadGroup reads the {id} path value. It renders 404 and returns false
// when the group does not exist.
func (s *Server) loadGroup(w http.ResponseWriter, r *http.Request) (store.Group, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderError(w, http.StatusNotFound)
		return store.Group{}, false
	}
	g, err := s.store.Group(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.renderError(w, http.StatusNotFound)
		return store.Group{}, false
	}
	if err != nil {
		s.serverError(w, err)
		return store.Group{}, false
	}
	return g, true
}

func (s *Server) handleGroupDelete(w http.ResponseWriter, r *http.Request) {
	g, ok := s.loadGroup(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteGroup(r.Context(), g.ID); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/groups", http.StatusSeeOther)
}

// --- Add and rename form ---

// groupForm is the template content of group_form.html.
type groupForm struct {
	ID    int64 // 0 when the form adds a group
	Name  string
	Error string
}

func (s *Server) handleGroupNewForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "group_form.html", pageData{Content: groupForm{}, Nav: "groups"})
}

func (s *Server) handleGroupCreate(w http.ResponseWriter, r *http.Request) {
	s.saveGroup(w, r, 0)
}

func (s *Server) handleGroupEditForm(w http.ResponseWriter, r *http.Request) {
	g, ok := s.loadGroup(w, r)
	if !ok {
		return
	}
	s.render(w, http.StatusOK, "group_form.html", pageData{Content: groupForm{ID: g.ID, Name: g.Name}, Nav: "groups"})
}

func (s *Server) handleGroupUpdate(w http.ResponseWriter, r *http.Request) {
	g, ok := s.loadGroup(w, r)
	if !ok {
		return
	}
	s.saveGroup(w, r, g.ID)
}

// saveGroup creates a group, or renames group id when id is not 0. It shows
// the form again when the posted name is not valid.
func (s *Server) saveGroup(w http.ResponseWriter, r *http.Request, id int64) {
	f := groupForm{ID: id, Name: strings.TrimSpace(r.FormValue("name"))}
	var err error
	switch {
	case f.Name == "":
		f.Error = "Enter a name."
	case utf8.RuneCountInString(f.Name) > maxNameLen:
		f.Error = nameTooLong
	case id == 0:
		err = s.store.CreateGroup(r.Context(), &store.Group{Name: f.Name})
	default:
		err = s.store.RenameGroup(r.Context(), id, f.Name)
	}
	switch {
	case errors.Is(err, store.ErrNameInUse):
		f.Error = "The name is already in use. Choose a different name."
	case errors.Is(err, store.ErrNotFound):
		s.renderError(w, http.StatusNotFound)
		return
	case err != nil:
		s.serverError(w, err)
		return
	}
	if f.Error != "" {
		s.render(w, http.StatusBadRequest, "group_form.html", pageData{Content: f, Nav: "groups"})
		return
	}
	http.Redirect(w, r, "/groups", http.StatusSeeOther)
}
