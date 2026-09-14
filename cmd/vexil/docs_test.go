package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/InAtTheGeekEnd/vexil/internal/brand"
)

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
