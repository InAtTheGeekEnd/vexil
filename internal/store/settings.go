package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Setting keys used by the application.
const (
	SettingPasswordHash = "password_hash"

	SettingBrandName      = "brand_name"
	SettingBrandAccent    = "brand_accent"
	SettingBrandPoweredBy = "brand_powered_by" // "1" or "0"
	SettingBrandLogo      = "brand_logo"       // base64 file content
	SettingBrandLogoType  = "brand_logo_type"  // media type of the logo
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

// Settings returns the values of the given keys. Missing keys are absent
// from the map.
func (s *Store) Settings(ctx context.Context, keys ...string) (map[string]string, error) {
	if len(keys) == 0 {
		return map[string]string{}, nil
	}
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, value FROM settings WHERE key IN (?`+strings.Repeat(",?", len(keys)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string, len(keys))
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetSetting inserts or replaces the value for key.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	return s.SetSettings(ctx, map[string]string{key: value})
}

// SetSettings inserts or replaces every key in one transaction.
func (s *Store) SetSettings(ctx context.Context, values map[string]string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range values {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO settings (key, value) VALUES (?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteSettings removes the given keys. Missing keys are not an error.
func (s *Store) DeleteSettings(ctx context.Context, keys ...string) error {
	for _, k := range keys {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, k); err != nil {
			return err
		}
	}
	return nil
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

// SetPasswordHash stores a new bcrypt hash and deletes every session except
// the one with keepTokenHash. An empty keepTokenHash deletes every session.
func (s *Store) SetPasswordHash(ctx context.Context, hash, keepTokenHash string) error {
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash <> ?`, keepTokenHash); err != nil {
		return err
	}
	return tx.Commit()
}
