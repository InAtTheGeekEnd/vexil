package check

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestHTTPCheckOpensANewConnection runs two checks against one server. The
// keyword makes each check read the whole body, which would let a client
// with keep-alive reuse the connection. Each check must open its own, so a
// renewed certificate or a new DNS answer shows at the next check.
func TestHTTPCheckOpensANewConnection(t *testing.T) {
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "all good")
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)

	for i := 1; i <= 2; i++ {
		if got := (&HTTP{URL: srv.URL, Keyword: "good"}).Check(context.Background()); !got.OK {
			t.Fatalf("check %d = %+v", i, got)
		}
	}
	if n := conns.Load(); n != 2 {
		t.Fatalf("two checks opened %d connections, want 2", n)
	}
}
