package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestReleaseSkipsPreReleases reads the release config. A pre-release tag
// must not move the Docker latest tag, and GoReleaser must publish it as a
// pre-release on GitHub.
func TestReleaseSkipsPreReleases(t *testing.T) {
	repo := filepath.Join("..", "..")
	workflow, err := os.ReadFile(filepath.Join(repo, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	latest := regexp.MustCompile(`(?m)^\s*type=raw,value=latest.*$`).FindString(string(workflow))
	if latest == "" {
		t.Fatal("release.yml has no latest tag")
	}
	if !strings.Contains(latest, "enable=${{ !contains(github.ref_name, '-') }}") {
		t.Fatalf("the latest tag moves for every tag: %q", strings.TrimSpace(latest))
	}

	goreleaser, err := os.ReadFile(filepath.Join(repo, ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^release:\n(\s+#.*\n)*\s+prerelease: auto$`).Match(goreleaser) {
		t.Fatal(".goreleaser.yaml does not set release.prerelease to auto")
	}
}
