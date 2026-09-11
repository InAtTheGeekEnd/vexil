// Package config reads the few settings that live outside the UI.
package config

import (
	"strings"
)

// Config holds the environment settings.
type Config struct {
	// Addr is the listen address. Default ":8080".
	Addr string
	// Data is the folder for the SQLite file and uploads. Default "./data".
	Data string
	// BaseURL is the public URL used in notification links. Default empty.
	BaseURL string
}

// Load builds a Config from a lookup function such as os.LookupEnv.
// Empty values fall back to the defaults.
func Load(lookup func(string) (string, bool)) Config {
	get := func(key, def string) string {
		if v, ok := lookup(key); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return def
	}
	return Config{
		Addr:    get("VEXIL_ADDR", ":8080"),
		Data:    get("VEXIL_DATA", "./data"),
		BaseURL: strings.TrimRight(get("VEXIL_BASE_URL", ""), "/"),
	}
}
