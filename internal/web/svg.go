package web

import (
	"fmt"
	"html/template"
	"math"
	"strings"
)

// chartPoint is one value on a response chart.
type chartPoint struct {
	Value float64
	Label string // tooltip text, for example "14:20 · 182 ms"
}

const (
	chartW = 600
	chartH = 160
	sparkW = 80
	sparkH = 24
)

// responseChart renders the SVG response chart from SPEC.md section 10.5:
// a smooth accent line, an area fill from 12% to 0% opacity, a faint
// baseline and hover data for chart.js. id must be unique on the page.
func responseChart(id string, points []chartPoint) template.HTML {
	const padX, padTop, padBottom = 4.0, 12.0, 4.0
	values := make([]float64, len(points))
	for i, p := range points {
		values[i] = p.Value
	}
	xs, ys := scalePoints(values, chartW, chartH, padX, padTop, padBottom)
	baseline := chartH - padBottom

	var hover []string
	for i, p := range points {
		hover = append(hover, fmt.Sprintf("%s:%s:%s", ftoa(xs[i]), ftoa(ys[i]), strings.ReplaceAll(p.Label, "|", " ")))
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="chart" viewBox="0 0 %d %d" preserveAspectRatio="none" role="img" aria-label="Response time chart" data-points="%s">`,
		chartW, chartH, template.HTMLEscapeString(strings.Join(hover, "|")))
	writeGradient(&b, id)
	fmt.Fprintf(&b, `<line class="baseline" x1="0" y1="%s" x2="%d" y2="%s"/>`, ftoa(baseline), chartW, ftoa(baseline))
	if len(xs) > 0 {
		line := smoothPath(xs, ys)
		fmt.Fprintf(&b, `<path class="area" fill="url(#%s)" d="%s L%s %s L%s %s Z"/>`,
			id, line, ftoa(xs[len(xs)-1]), ftoa(baseline), ftoa(xs[0]), ftoa(baseline))
		fmt.Fprintf(&b, `<path class="line" d="%s"/>`, line)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// sparkline renders the 80 x 24 px mini chart with no axes.
func sparkline(id string, values []float64) template.HTML {
	xs, ys := scalePoints(values, sparkW, sparkH, 1, 2, 1)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="sparkline" viewBox="0 0 %d %d" preserveAspectRatio="none" aria-hidden="true">`, sparkW, sparkH)
	writeGradient(&b, id)
	if len(xs) > 0 {
		line := smoothPath(xs, ys)
		fmt.Fprintf(&b, `<path class="area" fill="url(#%s)" d="%s L%s %d L%s %d Z"/>`,
			id, line, ftoa(xs[len(xs)-1]), sparkH, ftoa(xs[0]), sparkH)
		fmt.Fprintf(&b, `<path class="line" d="%s"/>`, line)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

func writeGradient(b *strings.Builder, id string) {
	fmt.Fprintf(b, `<defs><linearGradient id="%s" x1="0" y1="0" x2="0" y2="1">`+
		`<stop offset="0" stop-color="currentColor" stop-opacity="0.12"/>`+
		`<stop offset="1" stop-color="currentColor" stop-opacity="0"/>`+
		`</linearGradient></defs>`, template.HTMLEscapeString(id))
}

// scalePoints maps values onto SVG coordinates. The y range starts at zero
// so a flat, fast series sits low and a slow series sits high.
func scalePoints(values []float64, w, h, padX, padTop, padBottom float64) (xs, ys []float64) {
	n := len(values)
	if n == 0 {
		return nil, nil
	}
	maxV := 0.0
	for _, v := range values {
		if v > maxV {
			maxV = v
		}
	}
	if maxV <= 0 {
		maxV = 1
	}
	xs = make([]float64, n)
	ys = make([]float64, n)
	innerW := w - 2*padX
	innerH := h - padTop - padBottom
	for i, v := range values {
		if n == 1 {
			xs[i] = w / 2
		} else {
			xs[i] = padX + innerW*float64(i)/float64(n-1)
		}
		ys[i] = padTop + innerH*(1-math.Max(v, 0)/maxV)
	}
	return xs, ys
}

// smoothPath returns an SVG path through the points using Catmull-Rom
// splines converted to cubic Béziers.
func smoothPath(xs, ys []float64) string {
	n := len(xs)
	var b strings.Builder
	fmt.Fprintf(&b, "M%s %s", ftoa(xs[0]), ftoa(ys[0]))
	if n == 1 {
		return b.String()
	}
	at := func(i int) (float64, float64) {
		if i < 0 {
			i = 0
		}
		if i >= n {
			i = n - 1
		}
		return xs[i], ys[i]
	}
	for i := 0; i < n-1; i++ {
		x0, y0 := at(i - 1)
		x1, y1 := at(i)
		x2, y2 := at(i + 1)
		x3, y3 := at(i + 2)
		c1x := x1 + (x2-x0)/6
		c1y := y1 + (y2-y0)/6
		c2x := x2 - (x3-x1)/6
		c2y := y2 - (y3-y1)/6
		fmt.Fprintf(&b, " C%s %s %s %s %s %s", ftoa(c1x), ftoa(c1y), ftoa(c2x), ftoa(c2y), ftoa(x2), ftoa(y2))
	}
	return b.String()
}

func ftoa(f float64) string {
	s := fmt.Sprintf("%.1f", f)
	return strings.TrimSuffix(s, ".0")
}

// uptimeDay is one segment of an uptime bar.
type uptimeDay struct {
	Day       string  // YYYY-MM-DD
	Percent   float64 // 0 to 100
	Incidents int
	HasData   bool
}

// Class returns the CSS class for the segment color: --up at 100, --warn
// from 95 to 99.99, --down below 95, --border without data. The percentage
// is the one the tooltip shows, so a day with any incident time is never
// green.
func (d uptimeDay) Class() string {
	switch {
	case !d.HasData:
		return ""
	case d.Percent >= 100:
		return "seg-up"
	case d.Percent >= 95:
		return "seg-warn"
	default:
		return "seg-down"
	}
}

// Tip returns the tooltip text: date, uptime, incidents.
func (d uptimeDay) Tip() string {
	if !d.HasData {
		return d.Day + "\nNo data"
	}
	inc := "incidents"
	if d.Incidents == 1 {
		inc = "incident"
	}
	return fmt.Sprintf("%s\n%s%% uptime · %d %s", d.Day, formatPercent(d.Percent), d.Incidents, inc)
}

// formatPercent prints 100, 99.9, 99.95 or 87.2 without trailing zeros. It
// cuts, not rounds, so 99.999 prints as 99.99: only a full 100 prints as
// 100, and the colour of a segment or tile agrees with its number.
func formatPercent(p float64) string {
	s := fmt.Sprintf("%.2f", math.Floor(p*100)/100)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}
