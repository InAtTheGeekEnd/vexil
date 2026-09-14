package web

import (
	"strconv"
	"testing"
	"time"
)

// BenchmarkDetail measures the server response of the detail page of one
// monitor among 100, each with 30 days of checks at a 30-second interval:
// 86,400 check rows for the monitor. SPEC.md section 14 sets no target for
// the detail page.
// The setup takes a while, so run the benchmark on its own:
//
//	go test -run '^$' -bench BenchmarkDetail -benchtime 20x ./internal/web
func BenchmarkDetail(b *testing.B) {
	ids, serve := benchServer(b, 30)
	path := "/monitors/" + strconv.FormatInt(ids[0], 10)
	serve(path) // warm the SQLite page cache before the timer starts
	for b.Loop() {
		serve(path)
	}
	b.ReportMetric(float64(b.Elapsed())/float64(b.N)/float64(time.Millisecond), "ms/op")
}
