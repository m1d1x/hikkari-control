-- 073 — three steps of life for every consumable (roadmap 42 П9.11).
--
-- A domain, a machine (its address) and a cover site used to have two states:
-- working, and dead. Dead was the eviction threshold's word, so one bad
-- window — a flap, our own outage, a list that reached one operator for an
-- hour — sold a machine and wrote off a name for good. Now there are three:
--
--   working   — in the pool, handed out;
--   suspect   — the signals are bad but the ban is not proven. Out of the
--               pool at once (people are already hurting), replaced from the
--               pool, but not written off: a rare reprobe, apart from the
--               routine rounds, gives it a second chance;
--   dead      — the second chance failed (enough failed reprobes, or the
--               deadline passed). Final: the machine is handed back, the name
--               is not renewed, the cover is struck from the proven set.
--
-- The eviction threshold now leads to `suspect`; only a failed second chance
-- leads to `dead`. Returning is stricter than evicting: several clean reprobes
-- in a row, a probe interval apart, spanning most of a day at least.
--
-- The burned domain of §11 (072) was the first case of this; its two columns
-- move into the ledger below and the domain keeps only its status.

-- The ledger: one row per suspicion. A row stays after it ends, with how it
-- ended — the record of a second chance is the dataset for tuning the knobs.
CREATE TABLE entry_suspects (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- domain: entry_domains.domain; box: entry_boxes.name; cover: a cover
    -- site's host name.
    kind          text NOT NULL CHECK (kind IN ('domain', 'box', 'cover')),
    subject       text NOT NULL CHECK (subject <> ''),
    -- The cause that evicted it (domain_blocked, ip_banned, provider_down,
    -- destination): what a box dies of if the second chance fails.
    reason        text NOT NULL CHECK (reason <> ''),
    status        text NOT NULL DEFAULT 'suspect' CHECK (status IN ('suspect', 'returned', 'dead')),
    since         timestamptz NOT NULL DEFAULT now(),
    -- When it was last handed to a reprobe. Written when the reprobe is
    -- queued, not when it is answered, so the interval holds even when nothing
    -- answers.
    last_probe_at timestamptz,
    -- Clean reprobes in a row; a failed one resets it, one that says nothing
    -- (network_down, unknown) leaves it.
    clean_streak  integer NOT NULL DEFAULT 0 CHECK (clean_streak >= 0),
    -- Failed reprobes over the whole suspicion: a clean one in between does
    -- not forgive them — something that fails half its reprobes is not coming
    -- back.
    failed_probes integer NOT NULL DEFAULT 0 CHECK (failed_probes >= 0),
    ended_at      timestamptz,
    outcome       text NOT NULL DEFAULT '',
    CONSTRAINT entry_suspects_ended CHECK ((status = 'suspect') = (ended_at IS NULL))
);

-- One open suspicion per thing, and death is once: a dead subject never gets
-- another row.
CREATE UNIQUE INDEX entry_suspects_open ON entry_suspects (kind, subject) WHERE status = 'suspect';
CREATE UNIQUE INDEX entry_suspects_dead ON entry_suspects (kind, subject) WHERE status = 'dead';
-- The reprobe pass: per kind, the longest-unasked first.
CREATE INDEX entry_suspects_due ON entry_suspects (kind, coalesce(last_probe_at, since))
    WHERE status = 'suspect';

-- The burned names of 072 carry their streak over.
INSERT INTO entry_suspects (kind, subject, reason, since, last_probe_at, clean_streak)
SELECT 'domain', domain, 'domain_blocked', coalesce(burned_at, created_at), last_reprobe_at, reprobe_streak
FROM entry_domains WHERE status = 'burned';

DROP INDEX entry_domains_reprobe;
ALTER TABLE entry_domains
    DROP COLUMN last_reprobe_at,
    DROP COLUMN reprobe_streak;

-- 'burned' is a domain under suspicion; 'dead' is one whose second chance
-- failed: never drawn, never reprobed, not to be renewed.
ALTER TABLE entry_domains
    DROP CONSTRAINT entry_domains_status_check,
    ADD CONSTRAINT entry_domains_status_check
        CHECK (status IN ('buying', 'owned', 'assigned', 'burned', 'dead', 'failed'));

-- A machine under suspicion: its relays leave the pool as `suspect` — out of
-- every batch, out of the routine rounds and out of rollouts, records kept so
-- a return needs no re-signing ceremony. Not `draining`: that one belongs to
-- the rollout, which puts every draining relay back in the pool when an
-- update is cut off (rollout.go) — a suspect machine must not come back by a
-- restart.
ALTER TABLE entry_relays
    DROP CONSTRAINT entry_relays_status_check,
    ADD CONSTRAINT entry_relays_status_check
        CHECK (status IN ('pending', 'alive', 'testing', 'draining', 'suspect', 'dead')),
    DROP CONSTRAINT entry_relays_alive_has_record,
    ADD CONSTRAINT entry_relays_alive_has_record
        CHECK (status NOT IN ('alive', 'testing', 'draining', 'suspect') OR envelope_ct IS NOT NULL);

ALTER TABLE entry_grants
    DROP CONSTRAINT entry_grants_revoke_reason_check,
    ADD CONSTRAINT entry_grants_revoke_reason_check
        CHECK (revoke_reason IN ('relay_dead', 'drained', 'suspect'));

-- A machine's reprobe is a probe task of its own kind: its suspect relays,
-- answered like an admission probe, claimed on the prober's slow loop.
ALTER TABLE entry_probe_tasks
    DROP CONSTRAINT entry_probe_tasks_kind_check,
    ADD CONSTRAINT entry_probe_tasks_kind_check CHECK (kind IN ('admission', 'bench', 'domain', 'box'));

-- The knobs, per kind, beside the eviction thresholds (065). Defaults and why
-- they are what they are: internal/entry/secondchance.go.
--
--   *_probe_every_seconds — how often one suspect is reprobed;
--   *_return_clean        — clean reprobes in a row that return it;
--   *_dead_fails          — failed reprobes that write it off;
--   *_dead_after_seconds  — how long it may stay suspect at all.
--
-- Two rules the checks hold for every kind: a return spans most of a day at
-- least (the clean reprobes are a probe interval apart, and the first and
-- the last are 20 hours apart or more — one clean read is a path the filter
-- was not on that time), and the deadline leaves room for a return (a
-- deadline shorter than the clean streak would write off everything).
ALTER TABLE entry_detection_policy
    ADD COLUMN domain_probe_every_seconds int NOT NULL DEFAULT 86400
        CHECK (domain_probe_every_seconds BETWEEN 3600 AND 604800),
    ADD COLUMN domain_return_clean int NOT NULL DEFAULT 3
        CHECK (domain_return_clean BETWEEN 2 AND 14),
    ADD COLUMN domain_dead_fails int NOT NULL DEFAULT 14
        CHECK (domain_dead_fails BETWEEN 1 AND 60),
    ADD COLUMN domain_dead_after_seconds int NOT NULL DEFAULT 2592000
        CHECK (domain_dead_after_seconds BETWEEN 86400 AND 31536000),

    ADD COLUMN box_probe_every_seconds int NOT NULL DEFAULT 86400
        CHECK (box_probe_every_seconds BETWEEN 3600 AND 604800),
    ADD COLUMN box_return_clean int NOT NULL DEFAULT 2
        CHECK (box_return_clean BETWEEN 2 AND 14),
    ADD COLUMN box_dead_fails int NOT NULL DEFAULT 2
        CHECK (box_dead_fails BETWEEN 1 AND 60),
    ADD COLUMN box_dead_after_seconds int NOT NULL DEFAULT 259200
        CHECK (box_dead_after_seconds BETWEEN 86400 AND 31536000),

    ADD COLUMN cover_probe_every_seconds int NOT NULL DEFAULT 43200
        CHECK (cover_probe_every_seconds BETWEEN 3600 AND 604800),
    ADD COLUMN cover_return_clean int NOT NULL DEFAULT 3
        CHECK (cover_return_clean BETWEEN 2 AND 14),
    ADD COLUMN cover_dead_fails int NOT NULL DEFAULT 20
        CHECK (cover_dead_fails BETWEEN 1 AND 60),
    ADD COLUMN cover_dead_after_seconds int NOT NULL DEFAULT 1209600
        CHECK (cover_dead_after_seconds BETWEEN 86400 AND 31536000),

    ADD CONSTRAINT entry_detection_policy_domain_return_spans_a_day
        CHECK (domain_probe_every_seconds::bigint * (domain_return_clean - 1) >= 72000),
    ADD CONSTRAINT entry_detection_policy_box_return_spans_a_day
        CHECK (box_probe_every_seconds::bigint * (box_return_clean - 1) >= 72000),
    ADD CONSTRAINT entry_detection_policy_cover_return_spans_a_day
        CHECK (cover_probe_every_seconds::bigint * (cover_return_clean - 1) >= 72000),
    ADD CONSTRAINT entry_detection_policy_domain_deadline_leaves_a_return
        CHECK (domain_dead_after_seconds::bigint >= domain_probe_every_seconds::bigint * domain_return_clean),
    ADD CONSTRAINT entry_detection_policy_box_deadline_leaves_a_return
        CHECK (box_dead_after_seconds::bigint >= box_probe_every_seconds::bigint * box_return_clean),
    ADD CONSTRAINT entry_detection_policy_cover_deadline_leaves_a_return
        CHECK (cover_dead_after_seconds::bigint >= cover_probe_every_seconds::bigint * cover_return_clean);
