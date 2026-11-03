-- When the fleet pass last saw the pool at its target. The strikes of a
-- shortage count only the attempts after it: failures before a pool that was
-- filled are history of a shortage that ended, not of the one standing now.
ALTER TABLE entry_fleet_pools ADD COLUMN IF NOT EXISTS filled_at timestamptz;
