-- Monitor groups. The table is not named "groups" because GROUPS is a
-- keyword in window functions and would need quoting everywhere.
CREATE TABLE monitor_groups (
  id          INTEGER PRIMARY KEY,
  name        TEXT NOT NULL,
  position    INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL
);

-- Two groups cannot share a name, ignoring case. NOCASE folds only ASCII
-- letters, so the store also compares the other letters before a write.
CREATE UNIQUE INDEX monitor_groups_name ON monitor_groups(name COLLATE NOCASE);

-- NULL means the monitor is in no group, so every existing monitor starts
-- ungrouped.
ALTER TABLE monitors ADD COLUMN group_id INTEGER;
