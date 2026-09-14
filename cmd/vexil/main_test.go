package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/config"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// TestMain runs main instead of the tests when VEXIL_TEST_RUN_MAIN is set, so
// a test can start the binary as a user does. The variable holds the
// arguments.
func TestMain(m *testing.M) {
	if args, ok := os.LookupEnv("VEXIL_TEST_RUN_MAIN"); ok {
		os.Args = append([]string{os.Args[0]}, strings.Fields(args)...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// v1Schema is the schema of a v1 database: migrations 1 to 3 as v1 shipped
// them, with no AUTOINCREMENT and no foreign keys.
var v1Schema = []string{
	`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`,
	`CREATE TABLE monitors (
		id INTEGER PRIMARY KEY, name TEXT NOT NULL, type TEXT NOT NULL, target TEXT NOT NULL,
		keyword TEXT, expected_ip TEXT, push_token TEXT UNIQUE, interval_s INTEGER NOT NULL DEFAULT 60,
		public INTEGER NOT NULL DEFAULT 0, paused INTEGER NOT NULL DEFAULT 0,
		position INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL)`,
	`CREATE TABLE checks (monitor_id INTEGER NOT NULL, at INTEGER NOT NULL, ok INTEGER NOT NULL,
		latency_ms INTEGER, status_code INTEGER, error TEXT)`,
	`CREATE INDEX checks_monitor_at ON checks(monitor_id, at)`,
	`CREATE TABLE daily (monitor_id INTEGER NOT NULL, day TEXT NOT NULL, total INTEGER NOT NULL,
		ok INTEGER NOT NULL, avg_latency INTEGER, PRIMARY KEY (monitor_id, day))`,
	`CREATE TABLE incidents (id INTEGER PRIMARY KEY, monitor_id INTEGER NOT NULL,
		started_at INTEGER NOT NULL, ended_at INTEGER, reason TEXT)`,
	`CREATE TABLE channels (id INTEGER PRIMARY KEY, type TEXT NOT NULL, name TEXT NOT NULL,
		config TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1)`,
	`CREATE TABLE sessions (token_hash TEXT PRIMARY KEY, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL)`,
	`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	`ALTER TABLE monitors ADD COLUMN cert_warned_at INTEGER`,
	`ALTER TABLE channels ADD COLUMN last_error TEXT`,
	`ALTER TABLE channels ADD COLUMN last_error_at INTEGER`,
	`CREATE TABLE monitor_groups (id INTEGER PRIMARY KEY, name TEXT NOT NULL,
		position INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL)`,
	`CREATE UNIQUE INDEX monitor_groups_name ON monitor_groups(name COLLATE NOCASE)`,
	`ALTER TABLE monitors ADD COLUMN group_id INTEGER`,
	`INSERT INTO schema_migrations (version) VALUES (1), (2), (3)`,
	`INSERT INTO monitors (name, type, target, interval_s, created_at) VALUES ('Site', 'http', 'https://example.com', 60, 1700000000)`,
}

// TestV1DatabaseRefused starts the binary on a v1 database. It must not
// start: it exits with code 1 and prints one line that says the database is
// from v1 and must be recreated.
func TestV1DatabaseRefused(t *testing.T) {
	data := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(data, store.FileName)))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range v1Schema {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// A start that does not refuse would serve until the time limit.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "VEXIL_TEST_RUN_MAIN=", "VEXIL_DATA="+data, "VEXIL_ADDR=127.0.0.1:0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()

	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("exit = %v, want exit code 1; stderr:\n%s", err, stderr.String())
	}
	want := "The database in " + data + " is from v1 and must be recreated.\n"
	if stderr.String() != want || stdout.Len() != 0 {
		t.Fatalf("stderr = %q, stdout = %q; want only the line %q", stderr.String(), stdout.String(), want)
	}
}

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
