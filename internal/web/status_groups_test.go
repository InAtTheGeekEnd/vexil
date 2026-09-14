package web

import (
	"context"
	"html"
	"regexp"
	"strings"
	"testing"

	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// headings returns the unescaped text of the headings that re captures.
func headings(re *regexp.Regexp, page string) string {
	var out []string
	for _, m := range re.FindAllStringSubmatch(page, -1) {
		out = append(out, html.UnescapeString(m[1]))
	}
	return strings.Join(out, "|")
}

// TestStatusGroups renders the public page with groups. Only public monitors
// show, and a group without one is absent, name and all. Groups keep the
// dashboard order, a DOWN monitor stays in its group, and public monitors in
// no group come below the groups. A group name is escaped, and no target
// shows.
func TestStatusGroups(t *testing.T) {
	s, st, ids := seedServer(t,
		seed{name: "Site", public: true},
		seed{name: "Admin"},
		seed{name: "Vault"},
		seed{name: "Relay", public: true},
		seed{name: "Inbox", public: true, down: true},
		seed{name: "Homepage", public: true},
		seed{name: "Backups"},
	)
	ctx := context.Background()
	groups := map[string]int64{}
	for _, name := range []string{"Web <b>tier</b>", "Internal", "Mail"} {
		g := &store.Group{Name: name}
		if err := st.CreateGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
		groups[name] = g.ID
	}
	web, internal, mail := groups["Web <b>tier</b>"], groups["Internal"], groups["Mail"]
	if err := st.SaveLayout(ctx, []int64{mail, internal, web}, []store.Placement{
		{ID: ids[0], GroupID: web},
		{ID: ids[1], GroupID: web},
		{ID: ids[2], GroupID: internal},
		{ID: ids[3], GroupID: mail},
		{ID: ids[4], GroupID: mail},
		{ID: ids[5]},
		{ID: ids[6]},
	}); err != nil {
		t.Fatal(err)
	}
	ts, c := loggedIn(t, s, st)
	page := body(t, get(t, client(t), ts.URL+"/status"))
	dashboard := body(t, get(t, c, ts.URL+"/"))

	dash := headings(regexp.MustCompile(`<h2 id="group-\d+" title="[^"]*">([^<]*)</h2>`), dashboard)
	status := headings(regexp.MustCompile(`<h2 class="status-group-name" title="[^"]*">([^<]*)</h2>`), page)
	if dash != "Mail|Internal|Web <b>tier</b>" {
		t.Fatalf("dashboard groups = %q", dash)
	}
	if status != "Mail|Web <b>tier</b>" {
		t.Fatalf("status groups = %q, want the dashboard order without Internal", status)
	}

	inOrder(t, page, `title="Mail"`, `title="Relay"`, `title="Inbox"`, `title="Web &lt;b&gt;tier&lt;/b&gt;"`, `title="Site"`)
	relay, inbox := strings.Index(page, `title="Relay"`), strings.Index(page, `title="Inbox"`)
	if !strings.Contains(page[relay:inbox], "dot-down") {
		t.Error("Inbox is not DOWN in its place in Mail")
	}
	last, home := strings.LastIndex(page, `class="status-group-name"`), strings.Index(page, `title="Homepage"`)
	if home < last || !strings.Contains(page[last:home], "</section>") {
		t.Error("Homepage is not below the groups, outside every group")
	}
	if n := strings.Count(page, `class="status-row"`); n != 4 {
		t.Errorf("status rows = %d, want the 4 public monitors", n)
	}
	for _, leak := range []string{"Internal", "Admin", "Vault", "Backups", "127.0.0.1", "<b>tier", "data-group", "data-id", "mon-strip"} {
		if strings.Contains(page, leak) {
			t.Errorf("status page shows %q", leak)
		}
	}
}
