package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/InAtTheGeekEnd/vexil/internal/brand"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// recreateNote returns what the v2.0.0 release header must say. v2.0.0
// changes the database schema and does not migrate a v1 database, so the
// header gives the steps to recreate it, with the database file name and
// the message that vexil prints when it refuses a v1 database.
func recreateNote() []string {
	name, file := brand.ProductName, store.FileName
	return []string{
		"This release changes the database schema.",
		"A v1 database does not open: " + name + " refuses to start on one",
		"is from v1 and must be recreated.",
		"There is no migration.",
		"1. Stop " + name + ".",
		"2. Back up the data folder",
		"3. Delete the database file, `" + file + "`.",
		"`" + file + "-wal` and `" + file + "-shm`",
		"4. Start " + name + ".",
		"5. Open the web UI and set the password at the setup screen.",
	}
}

// headerProblems returns what is wrong with the release header for a tag.
// v2.0.0 must have the recreate note. Any other tag must have an empty
// header, so the note does not reach a later release.
func headerProblems(tag, header string) []string {
	if tag != "v2.0.0" {
		if strings.TrimSpace(header) != "" {
			return []string{fmt.Sprintf("release.header in .goreleaser.yaml is not empty for %s: remove the v2.0.0 note", tag)}
		}
		return nil
	}
	var out []string
	for _, want := range recreateNote() {
		if !strings.Contains(header, want) {
			out = append(out, fmt.Sprintf("the v2.0.0 release header does not say %q", want))
		}
	}
	return out
}

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

// TestReleaseHeader guards the header of the release notes. v2.0.0 must have
// the recreate note, and any other tag an empty header. The release workflow
// runs this test for the tag it releases, so a wrong header stops the
// release. A branch or pull request run has no tag: a header there must be
// the complete v2.0.0 note.
func TestReleaseHeader(t *testing.T) {
	note := strings.Join(recreateNote(), "\n")
	tests := []struct {
		name, tag, header string
		ok                bool
	}{
		{"v2.0.0 with the note", "v2.0.0", note, true},
		{"v2.0.0 without a header", "v2.0.0", "", false},
		{"v2.0.0 with part of the note", "v2.0.0", "There is no migration.", false},
		{"later tag with the note", "v2.0.1", note, false},
		{"later tag with another header", "v3.0.0", "Read this first.", false},
		{"later tag with an empty header", "v2.1.0", "", true},
	}
	for _, tc := range tests {
		if got := headerProblems(tc.tag, tc.header); (len(got) == 0) != tc.ok {
			t.Errorf("%s: problems %q, want ok %v", tc.name, got, tc.ok)
		}
	}

	header := releaseHeader(t)
	tag := "v2.0.0"
	if os.Getenv("GITHUB_REF_TYPE") == "tag" {
		tag = os.Getenv("GITHUB_REF_NAME")
	} else if header == "" {
		return
	}
	for _, p := range headerProblems(tag, header) {
		t.Error(p)
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
