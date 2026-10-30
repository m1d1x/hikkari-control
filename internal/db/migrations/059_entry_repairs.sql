-- 059 — repairs: every attempt the hub made to fix a box, and the rails that
-- keep those attempts from being a way to lose money and the pool
-- (roadmap 42 П9.6).
--
-- # Why a row per attempt, when the incident already has `playbook`
--
-- The incident says what was decided. This says what was tried, how often and
-- how it ended — and those are different numbers. An incident is one per (box,
-- cause) while it stands, on purpose; a repair can be attempted against it
-- three times, succeed on the third, and the fact that the first two failed is
-- the whole signal. Without it there is no cooldown (how long since the last
-- try), no wave (how many of this cause across the fleet in an hour) and no
-- "stop guessing" (this box has had three), which §4 of the plan calls
-- mandatory: an autopilot without them is a loop that buys machines.
--
-- # The rails read this table, nothing else
--
-- They are questions over these rows, not state of their own: no counters to
-- drift, no pause flags to forget to clear. A hub that restarts mid-repair
-- leaves a row stuck at `running`, which the next pass fails the way
-- entry_jobs already does — see the abandon query in internal/entry/jobs.go.
--
-- # §0.1 holds
--
-- A repair is about a machine. Nothing here names a client, a session or an
-- address; the box name and the cause are the whole subject.

CREATE TABLE entry_repairs (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- The incident this attempt was made against. Cascades with it: an
    -- incident's attempts are part of the incident, not a record that
    -- outlives it.
    incident_id uuid NOT NULL REFERENCES entry_incidents(id) ON DELETE CASCADE,
    -- Copied from the incident rather than joined to it. The rails ask "how
    -- many of this cause in the last hour" across incidents that may already
    -- be closed, and a wave must still be visible when its incidents are.
    box         text NOT NULL REFERENCES entry_boxes(name) ON DELETE CASCADE,
    cause       text NOT NULL CHECK (cause <> ''),
    -- The playbook as internal/entry/playbooks.go names it.
    playbook    text NOT NULL CHECK (playbook <> ''),
    -- auto   — the hub started it by itself.
    -- ask    — a person pressed the button the hub offered.
    -- manual — a person ran a repair the hub did not propose.
    decided_by  text NOT NULL CHECK (decided_by IN ('auto', 'ask', 'manual')),
    status      text NOT NULL DEFAULT 'running'
                CHECK (status IN ('running', 'done', 'failed')),
    -- The provisioning job that carried it out, when the repair needed a
    -- machine touched. A repair that only moves rows in the hub (taking banned
    -- ports out of the pool) has none, and that is the point of the column
    -- being nullable: "did this touch the box" is readable from it.
    job_id      uuid REFERENCES entry_jobs(id) ON DELETE SET NULL,
    error       text NOT NULL DEFAULT '',
    -- What it cost, in cents. A free repair is zero; the incident's own
    -- cost_cents is the sum of its attempts, which is why both exist.
    cost_cents  integer NOT NULL DEFAULT 0 CHECK (cost_cents >= 0),
    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    CONSTRAINT entry_repairs_finished_has_time
        CHECK ((status IN ('done', 'failed')) = (finished_at IS NOT NULL))
);

-- One repair at a time per box: the cooldown rail's first question, and the
-- reason a box being worked on is not picked up by the next pass a minute
-- later.
CREATE UNIQUE INDEX entry_repairs_one_running
    ON entry_repairs (box)
    WHERE status = 'running';

-- The cooldown and the three-strikes rail: this box, this cause, newest first.
CREATE INDEX entry_repairs_box_cause ON entry_repairs (box, cause, started_at DESC);

-- The wave rail: this cause across the fleet, inside a window.
CREATE INDEX entry_repairs_cause_window ON entry_repairs (cause, started_at DESC);

-- Why the hub stopped working on an incident by itself, in the words the
-- console shows: three failed attempts, a wave of this cause, a playbook it
-- does not have. Empty means the hub has not given up.
--
-- It lives on the incident and not in a status of its own because it is
-- orthogonal to one: an incident the hub handed over is still open, still
-- true, and still the thing a person is being asked about. What changes is who
-- is expected to act — which is exactly what decides whether the console shows
-- it in the queue or in the log.
ALTER TABLE entry_incidents ADD COLUMN handoff text NOT NULL DEFAULT '';

-- A repair that touches the box runs as an ordinary provisioning job, so the
-- console's live log, the one-job-per-box lock and the abandon-on-restart
-- sweep all apply to it unchanged. `rekey` is the first of them: new Reality
-- credentials on the same machine (internal/entry/repair.go).
ALTER TABLE entry_jobs
    DROP CONSTRAINT entry_jobs_kind_check,
    DROP CONSTRAINT entry_jobs_rollout_kind,
    ADD CONSTRAINT entry_jobs_kind_check
        CHECK (kind IN ('provision', 'canary', 'update', 'retire', 'rekey')),
    ADD CONSTRAINT entry_jobs_rollout_kind
        CHECK ((kind IN ('provision', 'retire', 'rekey')) = (rollout_id IS NULL));
