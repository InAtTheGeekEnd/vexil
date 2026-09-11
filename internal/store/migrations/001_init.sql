CREATE TABLE monitors (
  id          INTEGER PRIMARY KEY,
  name        TEXT NOT NULL,
  type        TEXT NOT NULL,
  target      TEXT NOT NULL,
  keyword     TEXT,
  expected_ip TEXT,
  push_token  TEXT UNIQUE,
  interval_s  INTEGER NOT NULL DEFAULT 60,
  public      INTEGER NOT NULL DEFAULT 0,
  paused      INTEGER NOT NULL DEFAULT 0,
  position    INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL
);

CREATE TABLE checks (
  monitor_id  INTEGER NOT NULL,
  at          INTEGER NOT NULL,
  ok          INTEGER NOT NULL,
  latency_ms  INTEGER,
  status_code INTEGER,
  error       TEXT
);
CREATE INDEX checks_monitor_at ON checks(monitor_id, at);

CREATE TABLE daily (
  monitor_id  INTEGER NOT NULL,
  day         TEXT NOT NULL,
  total       INTEGER NOT NULL,
  ok          INTEGER NOT NULL,
  avg_latency INTEGER,
  PRIMARY KEY (monitor_id, day)
);

CREATE TABLE incidents (
  id          INTEGER PRIMARY KEY,
  monitor_id  INTEGER NOT NULL,
  started_at  INTEGER NOT NULL,
  ended_at    INTEGER,
  reason      TEXT
);

CREATE TABLE channels (
  id          INTEGER PRIMARY KEY,
  type        TEXT NOT NULL,
  name        TEXT NOT NULL,
  config      TEXT NOT NULL,
  enabled     INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE sessions (
  token_hash  TEXT PRIMARY KEY,
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL
);

CREATE TABLE settings (
  key         TEXT PRIMARY KEY,
  value       TEXT NOT NULL
);
