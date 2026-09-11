package web

import (
	"fmt"
	"html/template"
	"math"
	"net/http"
	"time"
)

// styleguideContent is the sample data for /styleguide.
type styleguideContent struct {
	Chart      template.HTML
	ChartFlat  template.HTML
	Spark      template.HTML
	SparkSpiky template.HTML
	Uptime     []uptimeDay
	UptimeBad  []uptimeDay
}

func (s *Server) handleStyleguide(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "styleguide.html", pageData{Content: sampleStyleguide(time.Now().UTC())})
}

// sampleStyleguide builds deterministic sample data so the page looks the
// same on every load.
func sampleStyleguide(now time.Time) styleguideContent {
	var points []chartPoint
	var flat []chartPoint
	var spark, spiky []float64
	for i := 0; i < 48; i++ {
		t := now.Add(-time.Duration(47-i) * 30 * time.Minute)
		v := 180 + 40*math.Sin(float64(i)/4) + 15*math.Sin(float64(i)/1.7)
		if i == 30 {
			v = 420
		}
		points = append(points, chartPoint{Value: v, Label: fmt.Sprintf("%s · %d ms", t.Format("15:04"), int(v))})
		flat = append(flat, chartPoint{Value: 42 + 3*math.Sin(float64(i)/3), Label: fmt.Sprintf("%s · %d ms", t.Format("15:04"), 42)})
	}
	for i := 0; i < 24; i++ {
		spark = append(spark, 120+30*math.Sin(float64(i)/3))
		v := 90 + 20*math.Sin(float64(i)/2)
		if i == 17 {
			v = 380
		}
		spiky = append(spiky, v)
	}

	var good, bad []uptimeDay
	for i := 29; i >= 0; i-- {
		day := now.AddDate(0, 0, -i).Format("2006-01-02")
		g := uptimeDay{Day: day, Percent: 100, HasData: true}
		b := uptimeDay{Day: day, Percent: 100, HasData: true}
		switch i {
		case 21:
			b.Percent, b.Incidents = 99.31, 1
		case 12:
			b.Percent, b.Incidents = 91.7, 2
		case 11:
			b.Percent, b.Incidents = 98.9, 1
		}
		if i > 26 {
			g.HasData = false
			b.HasData = false
		}
		good = append(good, g)
		bad = append(bad, b)
	}

	return styleguideContent{
		Chart:      responseChart("sg-chart", points),
		ChartFlat:  responseChart("sg-chart-flat", flat),
		Spark:      sparkline("sg-spark", spark),
		SparkSpiky: sparkline("sg-spark-spiky", spiky),
		Uptime:     good,
		UptimeBad:  bad,
	}
}
