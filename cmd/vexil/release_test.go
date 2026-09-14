package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/InAtTheGeekEnd/vexil/internal/brand"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
)

// TestReleaseHeaderRecreate reads the header of the release notes. v2.0.0
// changes the database schema and does not migrate a v1 database, so the
// header must say so and give the steps to recreate it, with the database
// file name and the message that vexil prints when it refuses a v1 database.
func TestReleaseHeaderRecreate(t *testing.T) {
	config, err := os.ReadFile(filepath.Join("..", "..", ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	_, release, found := strings.Cut(string(config), "\nrelease:\n")
	if !found {
		t.Fatal(".goreleaser.yaml has no release section")
	}
	_, block, found := strings.Cut(release, "\n  header: |\n")
	if !found {
		t.Fatal("the release section has no header")
	}
	// The block ends at the first line that is not indented under header.
	var lines []string
	for _, line := range strings.Split(block, "\n") {
		if line != "" && !strings.HasPrefix(line, "    ") {
			break
		}
		lines = append(lines, strings.TrimPrefix(line, "    "))
	}
	header := strings.Join(lines, "\n")
	name, file := brand.ProductName, store.FileName
	for _, want := range []string{
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
	} {
		if !strings.Contains(header, want) {
			t.Errorf("the release header does not say %q", want)
		}
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
