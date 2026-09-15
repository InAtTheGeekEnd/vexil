-- No page reads the daily rows: uptime comes from the incidents, which are
-- kept forever, and the charts read hourly.
DROP TABLE daily;
-- samples is the number of successful checks behind avg_latency: its
-- weight, not an uptime figure.
ALTER TABLE hourly RENAME COLUMN ok TO samples;
