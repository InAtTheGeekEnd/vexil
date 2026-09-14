package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

func TestGroupLifecycle(t *testing.T) {
	s, st := newTestServer(t, Options{})
	ts, c := loggedIn(t, s, st)
	ctx := context.Background()

	// Empty state and the active nav item.
	b := body(t, get(t, c, ts.URL+"/groups"))
	for _, want := range []string{"No groups yet.", `href="/groups/new"`, `href="/groups" aria-current="page"`} {
		if !strings.Contains(b, want) {
			t.Errorf("empty groups page lacks %q", want)
		}
	}

	// Create. The name is trimmed.
	res := postForm(t, c, ts.URL+"/groups/new", url.Values{"name": {"  Production  "}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/groups" {
		t.Fatalf("create = %d -> %q", res.StatusCode, res.Header.Get("Location"))
	}
	groups, _ := st.Groups(ctx)
	if len(groups) != 1 || groups[0].Name != "Production" {
		t.Fatalf("saved groups = %+v", groups)
	}
	id := strconv.FormatInt(groups[0].ID, 10)

	// The list shows the name, the count and the delete question.
	b = body(t, get(t, c, ts.URL+"/groups"))
	for _, want := range []string{">Production<", ">No monitors<", `data-confirm="Delete Production? No monitors are in it."`, `href="/groups/` + id + `/edit"`} {
		if !strings.Contains(b, want) {
			t.Errorf("groups page lacks %q", want)
		}
	}

	// The rename form shows the saved name.
	if b := body(t, get(t, c, ts.URL+"/groups/"+id+"/edit")); !strings.Contains(b, `value="Production"`) {
		t.Fatal("rename form lacks the saved name")
	}

	if err := st.CreateGroup(ctx, &store.Group{Name: "Staging"}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, path, value, want string
	}{
		{"create without a name", "/groups/new", "   ", "Enter a name."},
		{"create with a long name", "/groups/new", strings.Repeat("a", 61), "60 characters or fewer"},
		{"create with a name in use", "/groups/new", "production", "The name is already in use."},
		{"rename without a name", "/groups/" + id + "/edit", "", "Enter a name."},
		{"rename to a name in use", "/groups/" + id + "/edit", "STAGING", "The name is already in use."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := postForm(t, c, ts.URL+tt.path, url.Values{"name": {tt.value}})
			b := body(t, res)
			if res.StatusCode != http.StatusBadRequest || !strings.Contains(b, tt.want) {
				t.Fatalf("status = %d, want 400 with %q", res.StatusCode, tt.want)
			}
			if !strings.Contains(b, `value="`+strings.TrimSpace(tt.value)+`"`) {
				t.Fatal("the form lost the typed name")
			}
		})
	}
	if groups, _ := st.Groups(ctx); len(groups) != 2 || groups[0].Name != "Production" {
		t.Fatalf("groups after bad posts = %+v", groups)
	}

	// Rename, also to the same name in another case.
	res = postForm(t, c, ts.URL+"/groups/"+id+"/edit", url.Values{"name": {"PRODUCTION"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("rename = %d", res.StatusCode)
	}
	if g, _ := st.Group(ctx, groups[0].ID); g.Name != "PRODUCTION" {
		t.Fatalf("renamed group = %+v", g)
	}

	// Delete.
	res = postForm(t, c, ts.URL+"/groups/"+id+"/delete", nil)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/groups" {
		t.Fatalf("delete = %d", res.StatusCode)
	}
	for _, tt := range []struct{ method, path string }{
		{http.MethodGet, "/groups/" + id + "/edit"},
		{http.MethodPost, "/groups/" + id + "/edit"},
		{http.MethodPost, "/groups/" + id + "/delete"},
		{http.MethodGet, "/groups/abc/edit"},
	} {
		var res *http.Response
		if tt.method == http.MethodGet {
			res = get(t, c, ts.URL+tt.path)
		} else {
			res = postForm(t, c, ts.URL+tt.path, url.Values{"name": {"Other"}})
		}
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", tt.method, tt.path, res.StatusCode)
		}
	}
}

func TestGroupText(t *testing.T) {
	tests := []struct {
		n              int
		count, confirm string
	}{
		{0, "No monitors", "Delete Web? No monitors are in it."},
		{1, "1 monitor", "Delete Web? 1 monitor moves out of the group. It is not deleted."},
		{3, "3 monitors", "Delete Web? 3 monitors move out of the group. They are not deleted."},
	}
	for _, tt := range tests {
		if got := monitorCount(tt.n); got != tt.count {
			t.Errorf("monitorCount(%d) = %q, want %q", tt.n, got, tt.count)
		}
		if got := deleteGroupConfirm("Web", tt.n); got != tt.confirm {
			t.Errorf("deleteGroupConfirm(%d) = %q, want %q", tt.n, got, tt.confirm)
		}
	}
}

func TestGroupsNeedLogin(t *testing.T) {
	s, st := newTestServer(t, Options{})
	setPassword(t, st)
	for _, path := range []string{"/groups", "/groups/new"} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
			t.Errorf("GET %s = %d -> %q, want 302 -> /login", path, rec.Code, rec.Header().Get("Location"))
		}
	}
}
