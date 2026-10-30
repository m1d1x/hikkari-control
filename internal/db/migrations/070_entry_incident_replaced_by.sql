-- Roadmap 42 П9.7: the machine a `replace_box` put in the pool, on the incident
-- it repaired.
--
-- Admission (`admitted_at`, migration 063) is written by the detector on the
-- first round a box reads healthy after its incident closed. A replacement
-- leaves the incident on the machine that was handed back, which has no relays
-- and never reads anything again — so the last stage of the console's timeline
-- never came for exactly the repair that changes the most. The proof that a
-- replacement landed is the replacement reading healthy, and this column is
-- what lets the detector find the incident from that box.
--
-- SET NULL and not CASCADE: forgetting the new machine (it was handed back in
-- turn, months later) must not take the history of the old one with it.
ALTER TABLE entry_incidents
    ADD COLUMN replaced_by text REFERENCES entry_boxes(name) ON DELETE SET NULL;

-- The detector asks this every round for every healthy box.
CREATE INDEX entry_incidents_replaced_by ON entry_incidents (replaced_by)
    WHERE replaced_by IS NOT NULL;
