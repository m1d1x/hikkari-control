-- 063 — when the replacement entered the pool (roadmap 42 К2).
--
-- # Why closed_at could not answer this
--
-- A repair closes its incident optimistically: the proof it worked is the next
-- probe round, not the repair's own exit code (internal/entry/repair.go). So
-- `closed_at` is the moment the hub finished acting, and the moment a person
-- cares about — the replacement passed a probe and is carrying traffic — came
-- minutes later and was recorded nowhere.
--
-- The incident timeline on the console's incidents screen reads failure →
-- diagnosis → repair → admission. Without this column the last stage would
-- have to be drawn from `closed_at`, which would quietly claim the pool was
-- whole the instant the hub stopped working rather than when it actually was.
--
-- # Who writes it
--
-- The detector, on the pass where the box reads healthy again
-- (internal/entry/detector.go → Service.MarkAdmitted). That is the same
-- evidence a person would accept, taken from the same probes, and it means
-- the column is written by the thing that observes rather than by the thing
-- that acts.

ALTER TABLE entry_incidents
    ADD COLUMN admitted_at timestamptz;

-- Nothing is admitted before it was closed. A box that recovered on its own
-- closes and is admitted in the same pass, which is honest: there was no
-- replacement to wait for.
ALTER TABLE entry_incidents
    ADD CONSTRAINT entry_incidents_admitted_after_close
        CHECK (admitted_at IS NULL OR closed_at IS NOT NULL);
