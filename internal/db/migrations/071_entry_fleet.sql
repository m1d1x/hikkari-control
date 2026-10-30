-- 071 — the fleet's own target, and an incident about a pool rather than a
-- machine (plan 42 §5, П9.10 scale_up; owner's decision 26.10: variant A).
--
-- Until now the hub bought a machine only to replace a banned one
-- (replace_box): a reaction to a death, and the death came with a box to hang
-- the incident on. scale_up is the second, independent reason — the pool of a
-- protocol has fewer live machines than the owner wants it to hold, now or by
-- the time a new one could be ordered and admitted. That shortage belongs to
-- no machine, so the incident and its repairs get a second kind of subject:
-- the pool, named "<proto>" or "hysteria2:<voice region>".
--
-- Why an incident and not a separate pass: `ask` — the default for anything
-- that spends money — exists only as the console's incident queue and its
-- button. A fleet shortage outside it would need a second queue, a second set
-- of strikes and cooldowns, and would be missing from the П9.8 dataset.
--
-- Nothing existing changes meaning: every row written so far names a box, and
-- keeps doing so. No target row means a target of zero, which means the hub
-- never orders a machine on its own — running this migration changes no
-- behaviour until the owner sets a target.

-- ── The subject of an incident: a box, or a pool ─────────────────────────

ALTER TABLE entry_incidents
    ALTER COLUMN box DROP NOT NULL,
    ADD COLUMN pool text CHECK (pool <> ''),
    ADD CONSTRAINT entry_incidents_one_subject CHECK ((box IS NULL) <> (pool IS NULL));

-- One open incident per (pool, cause), the same idempotence the (box, cause)
-- index gives the detector. Rows with a box carry a NULL pool and never meet
-- this index.
CREATE UNIQUE INDEX entry_incidents_one_open_pool
    ON entry_incidents (pool, cause)
    WHERE status IN ('open', 'acting');

ALTER TABLE entry_repairs
    ALTER COLUMN box DROP NOT NULL,
    ADD COLUMN pool text CHECK (pool <> ''),
    ADD CONSTRAINT entry_repairs_one_subject CHECK ((box IS NULL) <> (pool IS NULL));

-- The strikes and the cooldown of a pool, read like entry_repairs_box_cause.
CREATE INDEX entry_repairs_pool_cause ON entry_repairs (pool, cause, started_at DESC)
    WHERE pool IS NOT NULL;

-- At most one scale_up running across the whole fleet (§4: one order at a
-- time). A rail in the mechanic says the same; this index is what makes two
-- passes racing each other unable to both win.
CREATE UNIQUE INDEX entry_repairs_one_scale_up
    ON entry_repairs (playbook)
    WHERE status = 'running' AND playbook = 'scale_up';

-- ── The target ────────────────────────────────────────────────────────────

-- How many LIVE machines (not retired, at least one relay carrying people)
-- the owner wants each pool to hold. A pool is a protocol, and for voice a
-- protocol in front of one region — the same split entry_boxes makes
-- (049: a hysteria2 box serves one region and nothing else).
--
-- A setting, not an action: the console writes the number and the hub does
-- the ordering. Zero is allowed and means the same as no row — the owner
-- turning scale_up off for a pool without losing the row's history.
CREATE TABLE entry_fleet_targets (
    proto        text NOT NULL
                 CHECK (proto IN ('vless-reality-vision', 'hysteria2', 'vless-xhttp')),
    voice_region text NOT NULL DEFAULT ''
                 CHECK (voice_region = '' OR voice_region ~ '^[a-z0-9-]{1,32}$'),
    target_boxes int NOT NULL CHECK (target_boxes BETWEEN 0 AND 50),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    updated_by   text NOT NULL DEFAULT '',
    PRIMARY KEY (proto, voice_region),
    CONSTRAINT entry_fleet_targets_voice_has_region
        CHECK ((proto = 'hysteria2') = (voice_region <> ''))
);

-- ── The fleet's knobs, beside the other thresholds of the autopilot ──────

ALTER TABLE entry_detection_policy
    -- How far ahead the forecast looks. NULL is "auto": twice the median time
    -- from an order to admission at the hoster, measured on the machines the
    -- hub ordered. Set, it overrides the measurement — for a hoster with too
    -- few orders behind it to have a median.
    ADD COLUMN fleet_horizon_seconds int
        CHECK (fleet_horizon_seconds BETWEEN 600 AND 604800),
    -- How many machines the hub may set out to acquire in one hour, counting
    -- replacements and scale-ups together. A replacement never waits on it;
    -- a scale-up does, so a fleet that is losing machines spends its hour on
    -- replacing them before growing.
    ADD COLUMN fleet_orders_per_hour int NOT NULL DEFAULT 1
        CHECK (fleet_orders_per_hour BETWEEN 1 AND 10);
