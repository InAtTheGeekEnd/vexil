package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// TestSetupRace posts the first-run setup form from several clients at the
// same time. Exactly one may set the password and get a session. The others
// go to the login page, and the stored password is the one of the winner.
func TestSetupRace(t *testing.T) {
	s, st := newTestServer(t, Options{})
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)

	const n = 8
	type result struct {
		password string
		code     int
		location string
		session  bool
		err      error
	}
	results := make([]result, n)
	clients := make([]*http.Client, n)
	for i := range clients {
		clients[i] = client(t)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			password := fmt.Sprintf("password number %d", i)
			<-start
			res, err := clients[i].PostForm(ts.URL+"/setup", url.Values{"password": {password}, "confirm": {password}})
			r := result{password: password, err: err}
			if err == nil {
				r.code, r.location = res.StatusCode, res.Header.Get("Location")
				for _, ck := range res.Cookies() {
					r.session = r.session || (ck.Name == sessionCookie && ck.Value != "")
				}
				res.Body.Close()
			}
			results[i] = r
		}()
	}
	close(start)
	wg.Wait()

	var winner *result
	for i := range results {
		r := &results[i]
		switch {
		case r.err != nil:
			t.Fatalf("setup %d: %v", i, r.err)
		case r.code == http.StatusSeeOther && r.location == "/" && r.session:
			if winner != nil {
				t.Fatalf("two setups set the password: %q and %q", winner.password, r.password)
			}
			winner = r
		case r.code == http.StatusFound && r.location == "/login" && !r.session:
		default:
			t.Fatalf("setup %d = %d -> %q, session %v; want 303 -> / or 302 -> /login", i, r.code, r.location, r.session)
		}
	}
	if winner == nil {
		t.Fatal("no setup set the password")
	}
	hash, err := st.PasswordHash(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !checkPassword(hash, winner.password) {
		t.Fatal("the stored password is not the one of the setup that won")
	}
}
