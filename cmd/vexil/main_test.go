package main

import "testing"

func TestVersionString(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"set by ldflags", "v1.0.0", "v1.0.0"},
		{"not set", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := version
			version = tt.version
			defer func() { version = old }()
			got := versionString()
			if tt.want != "" && got != tt.want {
				t.Fatalf("versionString() = %q, want %q", got, tt.want)
			}
			if got == "" {
				t.Fatal("versionString() is empty")
			}
		})
	}
}
