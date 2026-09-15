package web

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/engine"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// benchServer builds a server whose store holds 100 monitors with 30 days of
// checks at the given interval in seconds, an incident a day, and the
// hourly and daily rows that the job writes. It returns the monitor ids and a function that serves
// one logged-in GET request. The engine is not started, so no check runs
// during the benchmark.
func benchServer(b *testing.B, interval int) ([]int64, func(path string)) {
	b.Helper()
	ctx := context.Background()
	dir := b.TempDir()
	st, err := store.Open(ctx, dir)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { st.Close() })

	const monitors, days = 100, 30
	var ids []int64
	for i := 1; i <= monitors; i++ {
		m := &store.Monitor{Name: "Service " + strconv.Itoa(i), Type: store.TypeHTTP, Target: "https://service" + strconv.Itoa(i) + ".example.com", IntervalS: interval}
		if err := st.CreateMonitor(ctx, m); err != nil {
			b.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	// One SQL statement per monitor inserts its checks far faster than one
	// InsertCheck call per row. Every 500th check fails, so the uptime bar
	// has real numbers.
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, store.FileName))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		b.Fatal(err)
	}
	now := time.Now().Unix()
	rows := days * 24 * 3600 / interval
	for _, id := range ids {
		if _, err := db.ExecContext(ctx, `
			WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i + 1 < ?)
			INSERT INTO checks (monitor_id, at, ok, latency_ms, status_code, error)
			SELECT ?, ? - i * ?,
				CASE WHEN i % 500 = 0 THEN 0 ELSE 1 END,
				80 + i % 40,
				CASE WHEN i % 500 = 0 THEN 500 ELSE 200 END,
				CASE WHEN i % 500 = 0 THEN 'HTTP 500' END
			FROM n`, rows, id, now, interval); err != nil {
			b.Fatal(err)
		}
	}
	// One incident of five minutes per monitor and day, so the uptime bars
	// and percentages have real numbers.
	if _, err := db.ExecContext(ctx, `
		WITH RECURSIVE d(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM d WHERE i + 1 < ?)
		INSERT INTO incidents (monitor_id, started_at, ended_at, reason)
		SELECT m.id, ? - i * 86400 - 3600, ? - i * 86400 - 3300, 'HTTP 500' FROM monitors m, d`,
		days, now, now); err != nil {
		b.Fatal(err)
	}
	if err := db.Close(); err != nil {
		b.Fatal(err)
	}
	// The hourly job writes the hourly and daily rows.
	if err := st.Rollup(ctx, time.Now()); err != nil {
		b.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := New(st, Options{Log: log, Engine: engine.New(st, engine.Options{Log: log})})
	if err != nil {
		b.Fatal(err)
	}
	hash, err := HashPassword(testPassword)
	if err != nil {
		b.Fatal(err)
	}
	if err := st.SetPasswordHash(ctx, hash, ""); err != nil {
		b.Fatal(err)
	}
	token, tokenHash, err := newSessionToken()
	if err != nil {
		b.Fatal(err)
	}
	if err := st.CreateSession(ctx, tokenHash, time.Now(), time.Now().Add(time.Hour)); err != nil {
		b.Fatal(err)
	}
	return ids, func(path string) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("GET %s = %d", path, rec.Code)
		}
	}
}

// BenchmarkDashboard measures the server response of the dashboard with 100
// monitors and 30 days of checks, at the default 60-second interval (4.32
// million check rows) and at 30 seconds (8.64 million). SPEC.md section 14
// sets the target at less than 50 ms. The sparkline and today's cell read
// the hourly rows and the checks of the current hour.
// The setup takes a while, so run the benchmark on its own:
//
//	go test -run '^$' -bench BenchmarkDashboard -benchtime 20x ./internal/web
func BenchmarkDashboard(b *testing.B) {
	for _, interval := range []int{60, 30} {
		b.Run(fmt.Sprintf("interval=%ds", interval), func(b *testing.B) {
			_, serve := benchServer(b, interval)
			serve("/") // warm the SQLite page cache before the timer starts
			for b.Loop() {
				serve("/")
			}
			b.ReportMetric(float64(b.Elapsed())/float64(b.N)/float64(time.Millisecond), "ms/op")
		})
	}
}
