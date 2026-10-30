-- 079 — the fleet sizes itself by load (plan 42 П9.16); the owner's target goes.
--
-- Owner's decision 27.10: a pool's machine count is not a number the owner
-- sets in the console — a knob nobody remembers to turn, and one blind to the
-- people using the pool. The hub derives it from the pool's forecast traffic
-- and the measured capacity of a machine (internal/entry/sizing.go). The only
-- knob left is the monthly money cap (entry_providers, 075).
--
-- What is left of entry_fleet_targets is the fleet pass's own memory of a
-- pool: since when it has held more than it needs, for the surplus handback.
-- So the table keeps its key and that column under a name that says what it
-- now is, and loses the owner's number and who set it.
--
-- Data: the rows on the stand are targets set by hand in tests; their surplus
-- marks are kept, the numbers are dropped on purpose.

ALTER TABLE entry_fleet_targets RENAME TO entry_fleet_pools;
ALTER TABLE entry_fleet_pools RENAME CONSTRAINT entry_fleet_targets_voice_has_region
    TO entry_fleet_pools_voice_has_region;
ALTER TABLE entry_fleet_pools RENAME CONSTRAINT entry_fleet_targets_pkey
    TO entry_fleet_pools_pkey;
ALTER TABLE entry_fleet_pools
    DROP COLUMN target_boxes,
    DROP COLUMN updated_at,
    DROP COLUMN updated_by;
