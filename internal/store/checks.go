package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Check is one row of the checks table.
type Check struct {
	MonitorID  int64
	At         time.Time
	OK         bool
	LatencyMS  int64
	StatusCode int
	Error      string
}

// InsertCheck stores one check result.
func (s *Store) InsertCheck(ctx context.Context, c Check) error {
	var latency, status, errText any
	if c.LatencyMS > 0 || c.OK {
		latency = c.LatencyMS
	}
	if c.StatusCode != 0 {
		status = c.StatusCode
	}
	if c.Error != "" {
		errText = c.Error
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO checks (monitor_id, at, ok, latency_ms, status_code, error) VALUES (?, ?, ?, ?, ?, ?)`,
		c.MonitorID, c.At.Unix(), c.OK, latency, status, errText)
	return err
}

// RecentChecks returns the newest n checks for a monitor, newest first.
func (s *Store) RecentChecks(ctx context.Context, monitorID int64, n int) ([]Check, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT monitor_id, at, ok, latency_ms, status_code, error
		FROM checks WHERE monitor_id = ? ORDER BY at DESC, rowid DESC LIMIT ?`, monitorID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Check
	for rows.Next() {
		var c Check
		var at int64
		var latency, status sql.NullInt64
		var errText sql.NullString
		if err := rows.Scan(&c.MonitorID, &at, &c.OK, &latency, &status, &errText); err != nil {
			return nil, err
		}
		c.At = time.Unix(at, 0)
		c.LatencyMS = latency.Int64
		c.StatusCode = int(status.Int64)
		c.Error = errText.String
		out = append(out, c)
	}
	return out, rows.Err()
}

// Incident is one row of the incidents table.
type Incident struct {
	ID        int64
	MonitorID int64
	StartedAt time.Time
	EndedAt   time.Time // zero while open
	Reason    string
}

// Open reports whether the incident has not ended.
func (i Incident) Open() bool { return i.EndedAt.IsZero() }

// OpenIncident starts an incident and returns its id.
func (s *Store) OpenIncident(ctx context.Context, monitorID int64, at time.Time, reason string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO incidents (monitor_id, started_at, reason) VALUES (?, ?, ?)`, monitorID, at.Unix(), reason)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CloseIncident ends every open incident of a monitor.
func (s *Store) CloseIncident(ctx context.Context, monitorID int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE incidents SET ended_at = ? WHERE monitor_id = ? AND ended_at IS NULL`, at.Unix(), monitorID)
	return err
}

// CurrentIncident returns the open incident of a monitor or ErrNotFound.
func (s *Store) CurrentIncident(ctx context.Context, monitorID int64) (Incident, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, monitor_id, started_at, ended_at, COALESCE(reason, '')
		FROM incidents WHERE monitor_id = ? AND ended_at IS NULL ORDER BY started_at DESC LIMIT 1`, monitorID)
	return scanIncident(row)
}

// Incidents returns the newest n incidents of a monitor, newest first.
func (s *Store) Incidents(ctx context.Context, monitorID int64, n int) ([]Incident, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, monitor_id, started_at, ended_at, COALESCE(reason, '')
		FROM incidents WHERE monitor_id = ? ORDER BY started_at DESC LIMIT ?`, monitorID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		inc, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inc)
	}
	return out, rows.Err()
}

func scanIncident(row interface{ Scan(...any) error }) (Incident, error) {
	var inc Incident
	var started int64
	var ended sql.NullInt64
	err := row.Scan(&inc.ID, &inc.MonitorID, &started, &ended, &inc.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return inc, ErrNotFound
	}
	if err != nil {
		return inc, err
	}
	inc.StartedAt = time.Unix(started, 0)
	if ended.Valid {
		inc.EndedAt = time.Unix(ended.Int64, 0)
	}
	return inc, nil
}

// LastSuccess returns the time of the newest successful check of a monitor,
// or the zero time when there is none.
func (s *Store) LastSuccess(ctx context.Context, monitorID int64) (time.Time, error) {
	var at sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT MAX(at) FROM checks WHERE monitor_id = ? AND ok = 1`, monitorID).Scan(&at)
	if err != nil || !at.Valid {
		return time.Time{}, err
	}
	return time.Unix(at.Int64, 0), nil
}
