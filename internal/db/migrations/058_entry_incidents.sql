-- 058 — incidents: what went wrong with a box, what was decided about it, and
-- how it ended (roadmap 42 П9.5).
--
-- An incident is the bridge between the two halves of the console. The
-- overview only observes and the tool pages only act; the one thing that
-- crosses is an incident — it is born from a diagnosis on the observing side
-- and carries the operator to the acting side with its cause in hand.
--
-- # Why this is a table and not a derived view
--
-- The diagnosis (internal/entry/diagnose.go) is recomputed from probes every
-- time anyone looks, and that is right for a diagnosis: it should always say
-- what the evidence says now. An incident is the opposite — it is a thing
-- that happened. What the hub believed at 14:02, what it decided, whether a
-- person overrode it and what it cost are not derivable from the current
-- state, and they are exactly the dataset that tells us later whether the
-- autopilot's choices were the right ones (П9.8 turns causes to `auto` only
-- after its suggestions matched reality for a week).
--
-- # One open incident per (box, cause)
--
-- A box whose IP is banned does not accumulate an incident per probe round.
-- The partial unique index below is what makes "open an incident for this" an
-- idempotent call: the detector can run every minute and say the same thing
-- every minute without the console filling with duplicates.
--
-- # Evidence is sealed, the shape of it is not
--
-- The evidence a diagnosis carried names relays and their verdicts — no
-- addresses, no clients — so it is plain. What is NOT here is anything about
-- the people on the box: an incident is about a machine, and §0.1 holds.

CREATE TABLE entry_incidents (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    box         text NOT NULL REFERENCES entry_boxes(name) ON DELETE CASCADE,
    -- The cause as internal/entry/diagnose.go named it. Not a CHECK list: a
    -- new cause is a code change there, and a migration to add a string to an
    -- enum would be a second place to keep in step.
    cause       text NOT NULL CHECK (cause <> ''),
    -- open      — the hub believes this is true now.
    -- acting    — a playbook is running against it.
    -- resolved  — the cause is gone, by repair or on its own.
    -- dismissed — a person said it is not worth acting on.
    status      text NOT NULL DEFAULT 'open'
                CHECK (status IN ('open', 'acting', 'resolved', 'dismissed')),
    -- The playbook chosen for it, when one was. Empty while nothing is
    -- decided and on an incident a person dismissed.
    playbook    text NOT NULL DEFAULT '',
    -- auto   — the hub ran the playbook itself.
    -- ask    — the hub proposed it and waited for a person.
    -- manual — a person acted without a proposal.
    -- Empty until something is decided. This column is the dataset П9.8 reads
    -- to decide whether a cause has earned `auto`.
    decided_by  text NOT NULL DEFAULT ''
                CHECK (decided_by IN ('', 'auto', 'ask', 'manual')),
    -- The one sentence the console shows, as the diagnosis wrote it.
    advice      text NOT NULL DEFAULT '',
    -- The verdicts the diagnosis rested on: {"ams1-1": "ip_banned", ...}.
    -- Kept as it was at the time, never refreshed — the point of a record is
    -- that it does not change its mind afterwards.
    evidence    jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- What it ended up costing, in cents: a replacement domain, a new
    -- machine. Zero when the repair was free (a restart, a key rotation).
    cost_cents  integer NOT NULL DEFAULT 0 CHECK (cost_cents >= 0),
    -- Free text for how it ended, written by whoever closed it.
    outcome     text NOT NULL DEFAULT '',
    opened_at   timestamptz NOT NULL DEFAULT now(),
    -- When a playbook started, so time-to-repair splits into time-to-decide
    -- and time-to-act. Those are the two numbers П9.7's chaos run measures.
    acted_at    timestamptz,
    closed_at   timestamptz,
    CONSTRAINT entry_incidents_closed_has_time
        CHECK ((status IN ('resolved', 'dismissed')) = (closed_at IS NOT NULL))
);

-- One open incident per (box, cause): the detector is then idempotent and may
-- run as often as it likes.
CREATE UNIQUE INDEX entry_incidents_one_open
    ON entry_incidents (box, cause)
    WHERE status IN ('open', 'acting');

-- The console's feed: newest first, open ones first of all.
CREATE INDEX entry_incidents_feed ON entry_incidents (opened_at DESC);

-- The dataset П9.8 reads: how a cause was decided, over time.
CREATE INDEX entry_incidents_by_cause ON entry_incidents (cause, opened_at DESC);
