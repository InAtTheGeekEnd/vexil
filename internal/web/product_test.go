package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/InAtTheGeekEnd/vexil/internal/brand"
)

// TestLoginHelpNamesTheBinary renames the brand. The reset help on the login
// page must still name the binary, because that is the command on the
// server.
func TestLoginHelpNamesTheBinary(t *testing.T) {
	s, st := newTestServer(t, Options{Brand: brand.Brand{Name: "Acme Status"}})
	setPassword(t, st)
	b := httptestRecord(s, http.MethodGet, "/login").Body.String()
	if !strings.Contains(b, "<code>"+brand.ProductName+" reset-password</code>") {
		t.Fatal("the login help does not name the binary")
	}
	if strings.Contains(b, "Acme Status reset-password") {
		t.Fatal("the login help names the brand as the command")
	}
	if !strings.Contains(b, "Acme Status") {
		t.Fatal("the login page does not show the brand name")
	}
}
