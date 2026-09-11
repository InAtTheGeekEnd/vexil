// Package web holds the embedded templates and static files.
package web

import "embed"

// Templates holds the html/template files.
//
//go:embed templates/*.html
var Templates embed.FS

// Static holds CSS, JS, fonts and images served under /static/.
//
//go:embed static
var Static embed.FS
