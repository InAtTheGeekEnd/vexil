package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Backup writes a copy of the database to a new file with VACUUM INTO. The
// copy is one consistent file that includes the contents of the WAL, and it
// can be made while the server runs. Backup does not overwrite a file.
func (s *Store) Backup(ctx context.Context, path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists: choose a new file name", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("write the backup: %w", err)
	}
	return nil
}
