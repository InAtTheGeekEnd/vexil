package store

import (
	"context"
	"database/sql"
	"errors"
)

// Setting keys used by the application.
const (
	SettingPasswordHash = "password_hash"
)

// GetSetting returns the value for key or ErrNotFound.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// SetSetting inserts or replaces the value for key.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// HasPassword reports whether the admin password is set.
func (s *Store) HasPassword(ctx context.Context) (bool, error) {
	_, err := s.GetSetting(ctx, SettingPasswordHash)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// PasswordHash returns the stored bcrypt hash or ErrNotFound.
func (s *Store) PasswordHash(ctx context.Context) (string, error) {
	return s.GetSetting(ctx, SettingPasswordHash)
}

// SetPasswordHash stores a new bcrypt hash and deletes every session.
func (s *Store) SetPasswordHash(ctx context.Context, hash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, SettingPasswordHash, hash); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		return err
	}
	return tx.Commit()
}
