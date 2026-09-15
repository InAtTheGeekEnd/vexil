package web

import (
	"strings"
	"testing"
	"time"
)

func TestResponseChart(t *testing.T) {
	tests := []struct {
		name   string
		points []chartPoint
		want   []string
		absent []string
	}{
		{
			name:   "empty",
			points: nil,
			want:   []string{`<svg class="chart"`, `class="baseline"`},
			absent: []string{`class="line"`},
		},
		{
			name:   "one point",
			points: []chartPoint{{Value: 100, Label: "12:00 · 100 ms"}},
			want:   []string{`class="line" d="M300 12"`, `data-points="300:12:12:00 · 100 ms"`},
		},
		{
			name:   "labels are escaped and separators removed",
			points: []chartPoint{{Value: 1, Label: `a|b<c`}, {Value: 2, Label: "d"}},
			want:   []string{`a b&lt;c`, `fill="url(#id)"`, ` C`},
			absent: []string{`a|b<c`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(responseChart("id", tt.points))
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in %s", w, got)
				}
			}
			for _, a := range tt.absent {
				if strings.Contains(got, a) {
					t.Errorf("unexpected %q in %s", a, got)
				}
			}
		})
	}
}

func TestSparkline(t *testing.T) {
	got := string(sparkline("sp", []float64{10, 20, 5, 30}))
	for _, w := range []string{`viewBox="0 0 80 24"`, `id="sp"`, `class="line"`, `class="area"`} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in %s", w, got)
		}
	}
	if got := string(sparkline("e", nil)); strings.Contains(got, "path") {
		t.Errorf("empty sparkline has a path: %s", got)
	}
}

func TestScalePoints(t *testing.T) {
	xs, ys := scalePoints([]float64{0, 50, 100}, 100, 50, 0, 0, 0)
	wantX := []float64{0, 50, 100}
	wantY := []float64{50, 25, 0}
	for i := range xs {
		if xs[i] != wantX[i] || ys[i] != wantY[i] {
			t.Fatalf("point %d = (%v, %v), want (%v, %v)", i, xs[i], ys[i], wantX[i], wantY[i])
		}
	}
	// Negative values clamp to the baseline, and all-zero series do not divide by zero.
	_, ys = scalePoints([]float64{0, 0}, 100, 50, 0, 0, 0)
	if ys[0] != 50 {
		t.Fatalf("zero series y = %v, want 50", ys[0])
	}
}

func TestUptimeDay(t *testing.T) {
	tests := []struct {
		day       uptimeDay
		wantClass string
		wantTip   string
	}{
		{uptimeDay{Day: "2026-09-01"}, "", "2026-09-01\nNo data"},
		{uptimeDay{Day: "2026-09-02", Percent: 100, HasData: true}, "seg-up", "2026-09-02\n100% uptime · 0 incidents"},
		{uptimeDay{Day: "2026-09-03", Percent: 99.95, Incidents: 1, HasData: true}, "seg-warn", "2026-09-03\n99.95% uptime · 1 incident"},
		{uptimeDay{Day: "2026-09-04", Percent: 95, Incidents: 2, HasData: true}, "seg-warn", "2026-09-04\n95% uptime · 2 incidents"},
		{uptimeDay{Day: "2026-09-05", Percent: 94.99, Incidents: 3, HasData: true}, "seg-down", "2026-09-05\n94.99% uptime · 3 incidents"},
		// A second of incident in a day: not green, and not shown as 100.
		{uptimeDay{Day: "2026-09-06", Percent: float64(86399) * 100 / 86400, Incidents: 1, HasData: true}, "seg-warn", "2026-09-06\n99.99% uptime · 1 incident"},
		{uptimeDay{Day: "2026-09-07", Percent: 94.999, Incidents: 1, HasData: true}, "seg-down", "2026-09-07\n94.99% uptime · 1 incident"},
		{uptimeDay{Day: "2026-09-08", Percent: 100, Incidents: 1, HasData: true}, "seg-up", "2026-09-08\n100% uptime · 1 incident"},
	}
	for _, tt := range tests {
		t.Run(tt.day.Day, func(t *testing.T) {
			if got := tt.day.Class(); got != tt.wantClass {
				t.Errorf("Class() = %q, want %q", got, tt.wantClass)
			}
			if got := tt.day.Tip(); got != tt.wantTip {
				t.Errorf("Tip() = %q, want %q", got, tt.wantTip)
			}
		})
	}
}

func TestSampleStyleguideIsDeterministic(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	a, b := sampleStyleguide(now), sampleStyleguide(now)
	if a.Chart != b.Chart || a.Spark != b.Spark || len(a.Uptime) != 30 || len(a.UptimeBad) != 30 {
		t.Fatal("sample data differs between calls or has the wrong size")
	}
}
