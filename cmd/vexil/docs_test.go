package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/InAtTheGeekEnd/vexil/internal/brand"
)

// TestRetryDocs reads SPEC 7.4. The service keeps alert retries in memory
// only, so the SPEC must say that a restart can lose a pending retry.
func TestRetryDocs(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "SPEC.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, section, _ := strings.Cut(string(b), "### 7.4 Delivery")
	section, _, _ = strings.Cut(section, "\n## ")
	for _, want := range []string{"Retries live in memory.", "A restart can lose a pending retry."} {
		if !strings.Contains(section, want) {
			t.Errorf("SPEC 7.4 does not say %q", want)
		}
	}
}

// TestBackupDocs reads SPEC.md and README.md. Both must tell the user to run
// the backup command. Neither may give a copy of the data folder as the
// backup: the database runs in WAL mode, so a copy of the live files can be
// broken.
func TestBackupDocs(t *testing.T) {
	command := brand.ProductName + " backup"
	copyFolder := regexp.MustCompile("(?i)\\bcop(y|ies) the `?data`? folder")
	for _, name := range []string{"SPEC.md", "README.md"} {
		b, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		if !strings.Contains(text, command) {
			t.Errorf("%s does not name the command %q", name, command)
		}
		if m := copyFolder.FindString(text); m != "" {
			t.Errorf("%s still gives a copy of the data folder as the backup: %q", name, m)
		}
	}
}
