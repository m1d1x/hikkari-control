-- 062 — the wave fuse as a thing of its own (roadmap 42 К2).
--
-- # What was wrong with the fuse before this table
--
-- When one cause broke three machines inside an hour the mechanic stopped
-- acting on it (internal/entry/repair.go, `rails`) and wrote why onto each
-- incident separately. The console then showed three look-alike incidents and
-- nothing at all about the fuse: an operator could read all three handoff
-- lines and still not learn that the autopilot had stood down on the cause as
-- a whole. The fuse existed in the hub's behaviour and nowhere in its state.
--
-- # Two consequences of making it a row
--
-- It is visible — one line in the queue, with the cause, the reason and how
-- many machines tripped it — and it is liftable: the operator says "I looked,
-- carry on", and that is a write, not a wait. Before this table the fuse also
-- un-blew itself, because the wave was counted over a rolling hour: the
-- autopilot resumed buying machines the moment the hour slid past, with nobody
-- having looked at why it stopped. A pause now ends only when a person ends
-- it.
--
-- # Why the history is kept
--
-- A lifted pause stays as a row. How often a cause trips the fuse, and how
-- long it sat paused, is exactly what П9.8 reads when it decides whether that
-- cause has earned the right to be repaired unattended at all.

CREATE TABLE entry_cause_pauses (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- The cause as internal/entry/diagnose.go names it. Not a CHECK list, for
    -- the same reason entry_incidents.cause is not one.
    cause     text NOT NULL CHECK (cause <> ''),
    -- The hub's own sentence about why it stood down, in the words the console
    -- shows.
    reason    text NOT NULL DEFAULT '',
    -- How many machines the cause had broken when the fuse blew. The number
    -- that makes the line readable: "4 machines in 18 minutes" is a wave, "1"
    -- would be a bug in the fuse.
    boxes     integer NOT NULL DEFAULT 0 CHECK (boxes >= 0),
    paused_at timestamptz NOT NULL DEFAULT now(),
    -- Set when a person lifted it; the admin id of whoever did.
    lifted_at timestamptz,
    lifted_by text NOT NULL DEFAULT '',
    CONSTRAINT entry_cause_pauses_lifted_has_time
        CHECK (lifted_by = '' OR lifted_at IS NOT NULL)
);

-- One standing pause per cause: the fuse blowing twice over the same cause is
-- the same fuse, so writing it is idempotent for the mechanic.
CREATE UNIQUE INDEX entry_cause_pauses_one_open
    ON entry_cause_pauses (cause)
    WHERE lifted_at IS NULL;

-- The history, newest first: how often this cause has tripped the fuse.
CREATE INDEX entry_cause_pauses_by_cause ON entry_cause_pauses (cause, paused_at DESC);
