package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Channel types.
const (
	ChannelEmail    = "email"
	ChannelSlack    = "slack"
	ChannelDiscord  = "discord"
	ChannelTelegram = "telegram"
	ChannelNtfy     = "ntfy"
	ChannelPushover = "pushover"
	ChannelWebhook  = "webhook"
)

// Channel is one row of the channels table. Config holds the fields of the
// channel type as strings, including secrets.
type Channel struct {
	ID          int64
	Type        string
	Name        string
	Config      map[string]string
	Enabled     bool
	LastError   string    // the last failed delivery, "" when the last one worked
	LastErrorAt time.Time // zero when LastError is ""
}

const channelCols = `id, type, name, config, enabled, COALESCE(last_error, ''), COALESCE(last_error_at, 0)`

func scanChannel(row interface{ Scan(...any) error }) (Channel, error) {
	var c Channel
	var config string
	var errAt int64
	err := row.Scan(&c.ID, &c.Type, &c.Name, &config, &c.Enabled, &c.LastError, &errAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal([]byte(config), &c.Config); err != nil {
		return c, err
	}
	if c.Config == nil {
		c.Config = map[string]string{}
	}
	if errAt != 0 {
		c.LastErrorAt = time.Unix(errAt, 0)
	}
	return c, nil
}

// CreateChannel inserts c and fills in ID.
func (s *Store) CreateChannel(ctx context.Context, c *Channel) error {
	config, err := json.Marshal(c.Config)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO channels (type, name, config, enabled) VALUES (?, ?, ?, ?)`,
		c.Type, c.Name, string(config), c.Enabled)
	if err != nil {
		return err
	}
	c.ID, err = res.LastInsertId()
	return err
}

// UpdateChannel saves the name and config of c.
func (s *Store) UpdateChannel(ctx context.Context, c Channel) error {
	config, err := json.Marshal(c.Config)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE channels SET name = ?, config = ? WHERE id = ?`, c.Name, string(config), c.ID)
	if err != nil {
		return err
	}
	return affected(res)
}

// SetChannelEnabled switches a channel on or off.
func (s *Store) SetChannelEnabled(ctx context.Context, id int64, enabled bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE channels SET enabled = ? WHERE id = ?`, enabled, id)
	if err != nil {
		return err
	}
	return affected(res)
}

// SetChannelError records the last failed delivery. An empty message
// clears it.
func (s *Store) SetChannelError(ctx context.Context, id int64, msg string, at time.Time) error {
	var errAt any
	if msg != "" {
		errAt = at.Unix()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE channels SET last_error = ?, last_error_at = ? WHERE id = ?`,
		nullIfEmpty(msg), errAt, id)
	if err != nil {
		return err
	}
	return affected(res)
}

// DeleteChannel removes a channel.
func (s *Store) DeleteChannel(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return affected(res)
}

// Channel returns one channel or ErrNotFound.
func (s *Store) Channel(ctx context.Context, id int64) (Channel, error) {
	return scanChannel(s.db.QueryRowContext(ctx, `SELECT `+channelCols+` FROM channels WHERE id = ?`, id))
}

// Channels returns every channel, oldest first.
func (s *Store) Channels(ctx context.Context) ([]Channel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+channelCols+` FROM channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// EnabledChannels returns the channels that receive alerts.
func (s *Store) EnabledChannels(ctx context.Context) ([]Channel, error) {
	all, err := s.Channels(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, c := range all {
		if c.Enabled {
			out = append(out, c)
		}
	}
	return out, nil
}
