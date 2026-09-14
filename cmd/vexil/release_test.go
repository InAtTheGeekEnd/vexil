package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestArchivesShipLinkedFiles reads the links and images in the README that
// point into the repository. Each must be a file that the release archives
// include, so an unpacked archive has no broken link. The font licence must
// be there: the OFL requires it to travel with the font.
func TestArchivesShipLinkedFiles(t *testing.T) {
	repo := filepath.Join("..", "..")
	readme, err := os.ReadFile(filepath.Join(repo, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(repo, ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var globs []string
	inFiles := false
	for _, line := range strings.Split(string(config), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "files:":
			inFiles = true
		case !inFiles, strings.HasPrefix(trimmed, "#"):
		case strings.HasPrefix(trimmed, "- "):
			globs = append(globs, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
		default:
			inFiles = false
		}
	}
	shipped := func(p string) bool {
		for _, g := range globs {
			if ok, _ := filepath.Match(g, p); ok {
				return true
			}
		}
		return false
	}

	local := 0
	for _, m := range regexp.MustCompile(`\]\(([^)\s]+)\)|src="([^"]+)"`).FindAllStringSubmatch(string(readme), -1) {
		target, _, _ := strings.Cut(m[1]+m[2], "#")
		if target == "" || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
			continue
		}
		local++
		if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(target))); err != nil {
			t.Errorf("the README links to %s, which does not exist", target)
		}
		if !shipped(target) {
			t.Errorf("the README links to %s, which the release archives leave out", target)
		}
	}
	if local == 0 {
		t.Fatal("found no README links into the repository")
	}
	if !shipped("web/static/fonts/OFL.txt") {
		t.Error("the release archives leave out the font licence")
	}
}

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
