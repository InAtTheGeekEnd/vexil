package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// releaseHeader returns release.header from .goreleaser.yaml, as a block or
// on one line, or "" when there is none.
func releaseHeader(t *testing.T) string {
	t.Helper()
	config, err := os.ReadFile(filepath.Join("..", "..", ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	_, release, found := strings.Cut(string(config), "\nrelease:\n")
	if !found {
		t.Fatal(".goreleaser.yaml has no release section")
	}
	if end := regexp.MustCompile(`(?m)^[^\s#]`).FindStringIndex(release); end != nil {
		release = release[:end[0]]
	}
	m := regexp.MustCompile(`(?m)^  header:[ \t]*(.*)\n?`).FindStringSubmatchIndex(release)
	if m == nil {
		return ""
	}
	if value := strings.TrimSpace(release[m[2]:m[3]]); !strings.HasPrefix(value, "|") && !strings.HasPrefix(value, ">") {
		return strings.Trim(value, `"'`)
	}
	// The block ends at the first line that is not indented under header.
	var lines []string
	for _, line := range strings.Split(release[m[1]:], "\n") {
		if line != "" && !strings.HasPrefix(line, "    ") {
			break
		}
		lines = append(lines, strings.TrimPrefix(line, "    "))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// TestReleaseHeader reads release.header from .goreleaser.yaml. It must be
// empty, whatever the tag: the v2.0.0 recreate note is gone and must not
// reach a later release. The release workflow runs this test, so a header
// stops the release.
func TestReleaseHeader(t *testing.T) {
	if header := releaseHeader(t); header != "" {
		t.Errorf("release.header in .goreleaser.yaml is not empty: remove it\n%s", header)
	}
}

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
