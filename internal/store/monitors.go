package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"time"
)

// Monitor types.
const (
	TypeHTTP = "http"
	TypeTCP  = "tcp"
	TypePing = "ping"
	TypeDNS  = "dns"
	TypePush = "push"
)

// Monitor is one row of the monitors table.
type Monitor struct {
	ID         int64
	Name       string
	Type       string
	Target     string
	Keyword    string
	ExpectedIP string
	PushToken  string
	IntervalS  int
	Public     bool
	Paused     bool
	Position   int   // the drag order inside the group, from 1
	GroupID    int64 // 0 when the monitor is in no group
	CreatedAt  time.Time
	// CertWarnedAt is the expiry of the certificate that the expiry warning
	// was sent for. Zero when no warning was sent.
	CertWarnedAt time.Time
}

// Interval returns the check interval as a duration.
func (m Monitor) Interval() time.Duration {
	return time.Duration(m.IntervalS) * time.Second
}

// NewPushToken returns 24 random bytes as URL-safe base64.
func NewPushToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

const monitorCols = `id, name, type, target, COALESCE(keyword, ''), COALESCE(expected_ip, ''),
	COALESCE(push_token, ''), interval_s, public, paused, position, created_at, COALESCE(cert_warned_at, 0),
	COALESCE(group_id, 0)`

func scanMonitor(row interface{ Scan(...any) error }) (Monitor, error) {
	var m Monitor
	var created, warned int64
	err := row.Scan(&m.ID, &m.Name, &m.Type, &m.Target, &m.Keyword, &m.ExpectedIP,
		&m.PushToken, &m.IntervalS, &m.Public, &m.Paused, &m.Position, &created, &warned, &m.GroupID)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	m.CreatedAt = time.Unix(created, 0)
	if warned != 0 {
		m.CertWarnedAt = time.Unix(warned, 0)
	}
	return m, err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// CreateMonitor inserts m and fills in ID, CreatedAt and, for push monitors,
// PushToken.
func (s *Store) CreateMonitor(ctx context.Context, m *Monitor) error {
	if m.IntervalS <= 0 {
		m.IntervalS = 60
	}
	if m.Type == TypePush && m.PushToken == "" {
		tok, err := NewPushToken()
		if err != nil {
			return err
		}
		m.PushToken = tok
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO monitors
		(name, type, target, keyword, expected_ip, push_token, interval_s, public, paused, position, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, (SELECT COALESCE(MAX(position), 0) + 1 FROM monitors), ?)`,
		m.Name, m.Type, m.Target, nullIfEmpty(m.Keyword), nullIfEmpty(m.ExpectedIP), nullIfEmpty(m.PushToken),
		m.IntervalS, m.Public, m.Paused, m.CreatedAt.Unix())
	if err != nil {
		return err
	}
	m.ID, err = res.LastInsertId()
	return err
}

// UpdateMonitor saves the editable fields of m.
func (s *Store) UpdateMonitor(ctx context.Context, m Monitor) error {
	res, err := s.db.ExecContext(ctx, `UPDATE monitors SET
		name = ?, type = ?, target = ?, keyword = ?, expected_ip = ?, interval_s = ?, public = ?, paused = ?
		WHERE id = ?`,
		m.Name, m.Type, m.Target, nullIfEmpty(m.Keyword), nullIfEmpty(m.ExpectedIP), m.IntervalS, m.Public, m.Paused, m.ID)
	if err != nil {
		return err
	}
	return affected(res)
}

// SetPaused pauses or resumes a monitor.
func (s *Store) SetPaused(ctx context.Context, id int64, paused bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE monitors SET paused = ? WHERE id = ?`, paused, id)
	if err != nil {
		return err
	}
	return affected(res)
}

// SetCertWarned records that the expiry warning for the certificate that
// expires at expiry was sent.
func (s *Store) SetCertWarned(ctx context.Context, id int64, expiry time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE monitors SET cert_warned_at = ? WHERE id = ?`, expiry.Unix(), id)
	if err != nil {
		return err
	}
	return affected(res)
}

// DeleteMonitor removes a monitor and all its data.
func (s *Store) DeleteMonitor(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM checks WHERE monitor_id = ?`,
		`DELETE FROM daily WHERE monitor_id = ?`,
		`DELETE FROM incidents WHERE monitor_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM monitors WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if err := affected(res); err != nil {
		return err
	}
	return tx.Commit()
}

// Monitor returns one monitor or ErrNotFound.
func (s *Store) Monitor(ctx context.Context, id int64) (Monitor, error) {
	return scanMonitor(s.db.QueryRowContext(ctx, `SELECT `+monitorCols+` FROM monitors WHERE id = ?`, id))
}

// MonitorByPushToken returns the push monitor for token or ErrNotFound.
func (s *Store) MonitorByPushToken(ctx context.Context, token string) (Monitor, error) {
	return scanMonitor(s.db.QueryRowContext(ctx, `SELECT `+monitorCols+` FROM monitors WHERE push_token = ?`, token))
}

// Monitors returns every monitor in display order.
func (s *Store) Monitors(ctx context.Context) ([]Monitor, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+monitorCols+` FROM monitors ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Monitor
	for rows.Next() {
		m, err := scanMonitor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func affected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// eachRow runs query in tx and calls fn for each row.
func eachRow(ctx context.Context, tx *sql.Tx, query string, fn func(*sql.Rows) error) error {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Placement puts a monitor in a group. GroupID 0 means no group.
type Placement struct {
	ID      int64
	GroupID int64
}

// SaveLayout saves the dashboard layout in one transaction. groups sets the
// position of each group to its index. monitors lists monitors in display
// order: each one moves into its group and takes the next free position in
// that group, from 1. An unknown group means no group. A monitor missing
// from monitors keeps its group and position, and the listed monitors of
// that group skip its position, so it keeps its place among them.
func (s *Store) SaveLayout(ctx context.Context, groups []int64, monitors []Placement) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range groups {
		if _, err := tx.ExecContext(ctx, `UPDATE monitor_groups SET position = ? WHERE id = ?`, i+1, id); err != nil {
			return err
		}
	}

	known := map[int64]bool{0: true} // group ids; 0 is no group
	err = eachRow(ctx, tx, `SELECT id FROM monitor_groups`, func(rows *sql.Rows) error {
		var id int64
		err := rows.Scan(&id)
		known[id] = true
		return err
	})
	if err != nil {
		return err
	}

	listed := make(map[int64]bool, len(monitors))
	for _, p := range monitors {
		listed[p.ID] = true
	}
	taken := map[int64]map[int]bool{} // the positions of unlisted monitors, by group
	err = eachRow(ctx, tx, `SELECT id, COALESCE(group_id, 0), position FROM monitors`, func(rows *sql.Rows) error {
		var id, group int64
		var pos int
		if err := rows.Scan(&id, &group, &pos); err != nil {
			return err
		}
		if !listed[id] {
			if taken[group] == nil {
				taken[group] = map[int]bool{}
			}
			taken[group][pos] = true
		}
		return nil
	})
	if err != nil {
		return err
	}

	next := map[int64]int{} // the last position given out, by group
	for _, p := range monitors {
		group := p.GroupID
		if !known[group] {
			group = 0
		}
		pos := next[group] + 1
		for taken[group][pos] {
			pos++
		}
		next[group] = pos
		var groupID any // NULL for no group
		if group != 0 {
			groupID = group
		}
		if _, err := tx.ExecContext(ctx, `UPDATE monitors SET group_id = ?, position = ? WHERE id = ?`, groupID, pos, p.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
