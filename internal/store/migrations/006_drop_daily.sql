-- No page reads the daily rows: uptime comes from the incidents, which are
-- kept forever, and the charts read hourly.
DROP TABLE daily;
