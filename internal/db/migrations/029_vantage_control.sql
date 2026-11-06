-- The prober's own health, read from inside Russia (vantage_control.go).
--
-- A new observation point is not made to sit out a probation. It is admitted
-- on a clean run Russia-to-Russia and then watched: every few minutes it
-- reaches for sites that are up for everyone inside Russia and reports how
-- many answered. Two failed controls in a row take the slot out of the fleet
-- (status 'retired', kicked_reason says why); a passed one clears the count.
ALTER TABLE entry_vantages
    ADD COLUMN IF NOT EXISTS control_at     timestamptz,
    ADD COLUMN IF NOT EXISTS control_fails  integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS control_note   text    NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS kicked_reason  text    NOT NULL DEFAULT '';
