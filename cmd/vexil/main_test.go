package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/InAtTheGeekEnd/vexil/internal/config"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

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

// TestReadyzURL maps listen addresses to the URL that the healthcheck
// command asks. An address with no host or on every interface is asked on
// 127.0.0.1. A bad address is an error.
func TestReadyzURL(t *testing.T) {
	tests := []struct {
		addr    string
		want    string
		wantErr bool
	}{
		{":8080", "http://127.0.0.1:8080/readyz", false},
		{"0.0.0.0:9000", "http://127.0.0.1:9000/readyz", false},
		{"[::]:8080", "http://127.0.0.1:8080/readyz", false},
		{"127.0.0.1:18091", "http://127.0.0.1:18091/readyz", false},
		{"192.168.1.5:80", "http://192.168.1.5:80/readyz", false},
		{"[::1]:8080", "http://[::1]:8080/readyz", false},
		{"localhost:8080", "http://localhost:8080/readyz", false},
		{"8080", "", true},
		{"", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			got, err := readyzURL(tt.addr)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("readyzURL(%q) = %q, %v; want %q, error %v", tt.addr, got, err, tt.want, tt.wantErr)
			}
		})
	}
}

// TestBackup runs the backup command while the database is open, as it is
// while the server runs. A second run to the same file, a missing file name
// and a data folder with no database must fail.
func TestBackup(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	st, err := store.Open(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateMonitor(ctx, &store.Monitor{Name: "API", Type: store.TypeHTTP, Target: "https://api.example.com", IntervalS: 60}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Data: data}
	path := filepath.Join(t.TempDir(), "backup.db")

	if err := backup(cfg, path); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
		t.Fatalf("backup file: %v", err)
	}

	empty := t.TempDir()
	tests := []struct {
		name    string
		cfg     config.Config
		path    string
		wantErr string
	}{
		{"same file again", cfg, path, "already exists"},
		{"no file name", cfg, "", "name a new file"},
		{"no database", config.Config{Data: empty}, filepath.Join(t.TempDir(), "b.db"), "no database in"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := backup(tt.cfg, tt.path)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(empty, store.FileName)); err == nil {
		t.Fatal("the backup command created a database in the empty folder")
	}
}
