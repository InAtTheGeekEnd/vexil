-- The periods a monitor was paused. A paused monitor is not live, so its
-- uptime leaves these periods out. ended_at is NULL while the pause lasts.
CREATE TABLE pauses (
  monitor_id  INTEGER NOT NULL,
  started_at  INTEGER NOT NULL,
  ended_at    INTEGER,
  FOREIGN KEY (monitor_id) REFERENCES monitors(id) ON DELETE CASCADE
);
CREATE INDEX pauses_monitor_started ON pauses(monitor_id, started_at);
-- A monitor paused before this table existed starts its pause now, because
-- when the user paused it is not recorded.
INSERT INTO pauses (monitor_id, started_at)
  SELECT id, CAST(strftime('%s', 'now') AS INTEGER) FROM monitors WHERE paused = 1;
