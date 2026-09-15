-- No page counts checks any more: uptime comes from the incidents and the
-- pauses. hourly keeps ok, the number of successful checks, because it is
-- the weight of avg_latency in the 30-day chart, the response tile and the
-- daily row.
ALTER TABLE daily DROP COLUMN total;
ALTER TABLE daily DROP COLUMN ok;
ALTER TABLE hourly DROP COLUMN total;
