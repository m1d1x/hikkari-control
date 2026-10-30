-- 075 — the money gate in Postgres, and the fleet's consumables told apart
-- (roadmap 42: audit fixes В3, В4, С1; П9.11б, owner's decisions 27.10).
--
-- What was wrong (AUDIT/42-autopilot.md):
--
--   В3  The machine budget was read under an in-process mutex released before
--       the box row was written, so two orders (two repairs, or the hub and
--       the one-shot CLI, which is a second process) both read the fleet
--       without the other and both went through. The domain purchase already
--       reserved its spend in Postgres under an advisory lock; a machine
--       order now does the same — a row with its monthly price, written
--       before the hoster is asked, and counted by the budget from then on.
--   С1  A machine the hoster would not delete left the budget the moment it
--       was retired, and an instance whose order failed after it was created
--       (no address in time, the box row refused) was in no budget at all.
--       Both are rent being paid; both are counted until the hoster confirms
--       the machine is gone, and a background pass keeps asking it to delete.
--   В4  A domain purchase whose answer never arrived was written `failed` and
--       dropped out of the month's spend, though the registrar may well have
--       charged it. "No answer" is now `unknown`: counted, and retried with
--       the key it was first sent with, which the registrar replays.

-- ── A machine order: the reservation before the charge ──────────────────

-- One row per attempt to rent a machine, written under an advisory lock
-- (internal/entry/hosting.go) before the hoster is asked. The budget counts
-- the rows that are, or may be, a machine being paid for that no box row
-- prices yet:
--
--   reserved   the money is held; the hoster has not answered yet, or
--              answered so that nobody knows whether it created anything;
--   created    the hoster created the instance (its id is known); the box row
--              is not written yet;
--   undeleted  the order failed after the instance existed and the hoster has
--              not (yet) confirmed deleting it.
--
-- and not the ones that are not:
--
--   recorded   a box row carries the machine and its price now;
--   released   nothing is rented: the hoster confirmed the delete, or there
--              was never an instance;
--   refused    the hoster answered no — nothing was created, nothing charged.
--
-- Every row but `refused` counts against the hourly and daily order limits
-- (the policy below): those limit attempts to spend, whichever path made them
-- — the fleet's scale_up, the console's order, the CLI.
CREATE TABLE entry_box_orders (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- The box name the order intends, and the label it puts on the instance
    -- (nekko-<name>): how an answer that never arrived is found again.
    name            text NOT NULL CHECK (name ~ '^[a-z]+[0-9]+$' AND length(name) <= 24),
    provider        text NOT NULL CHECK (provider <> ''),
    provider_region text NOT NULL CHECK (provider_region <> ''),
    plan            text NOT NULL CHECK (plan <> ''),
    monthly_cents   integer NOT NULL CHECK (monthly_cents > 0),
    status          text NOT NULL DEFAULT 'reserved'
                    CHECK (status IN ('reserved', 'created', 'undeleted', 'recorded', 'released', 'refused')),
    instance_id     text CHECK (instance_id <> ''),
    error           text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT entry_box_orders_created_has_instance
        CHECK (status NOT IN ('created', 'undeleted', 'recorded') OR instance_id IS NOT NULL)
);

-- Two orders in flight never take the same name: the label is how a lost
-- answer is found, and the box row's primary key would refuse the second
-- anyway — after it had been paid for.
CREATE UNIQUE INDEX entry_box_orders_name_in_flight ON entry_box_orders (name)
    WHERE status IN ('reserved', 'created', 'undeleted');

-- The budget and the reconciling pass read the open rows; the limits read the
-- recent ones.
CREATE INDEX entry_box_orders_open ON entry_box_orders (status, updated_at)
    WHERE status IN ('reserved', 'created', 'undeleted');
CREATE INDEX entry_box_orders_recent ON entry_box_orders (created_at)
    WHERE status <> 'refused';

-- ── A retired machine is paid for until the hoster lets it go ───────────

-- When the hoster confirmed the machine of a retired box gone (deleted, or
-- not there to delete). A retired box the hub ordered with this still NULL
-- is retired-but-undeleted: counted by the budget, shown as debt by the
-- console, and deleted again by the mechanic's pass.
--
-- Not backfilled: whether a box retired before today was deleted at its
-- hoster is not something the row knows. The pass asks the hoster, which
-- answers "gone" for the ones that are — at no cost — and deletes the ones
-- that are not, which is the leak this column is for.
ALTER TABLE entry_boxes ADD COLUMN released_at timestamptz;

CREATE INDEX entry_boxes_undeleted ON entry_boxes (retired_at)
    WHERE retired_at IS NOT NULL AND provider_instance_id IS NOT NULL AND released_at IS NULL;

-- ── A domain purchase whose answer never came ───────────────────────────

-- 'unknown': the purchase was sent and no answer came back. Counted in the
-- month's spend (the registrar may have charged it), never drawn, and retried
-- with its stored idempotency key until the registrar's answer — replayed for
-- 24 h — says which it was.
ALTER TABLE entry_domains
    DROP CONSTRAINT entry_domains_status_check,
    ADD CONSTRAINT entry_domains_status_check
        CHECK (status IN ('buying', 'unknown', 'owned', 'assigned', 'burned', 'dead', 'failed'));

-- ── The knobs: one limit on orders, the surplus, the domain pool ────────

ALTER TABLE entry_detection_policy
    -- The daily half of the order limit; fleet_orders_per_hour (071) is the
    -- hourly half. Both now count every machine order, whatever path made it
    -- (owner's decision 27.10: one limit for all the ways to spend).
    ADD COLUMN fleet_orders_per_day int NOT NULL DEFAULT 6
        CHECK (fleet_orders_per_day BETWEEN 1 AND 50),
    -- How long a pool may hold more live machines than it needs before the
    -- hub hands the oldest surplus one back (owner's decision 27.10: hourly
    -- rent does not pile up).
    ADD COLUMN fleet_surplus_hold_seconds int NOT NULL DEFAULT 86400
        CHECK (fleet_surplus_hold_seconds BETWEEN 3600 AND 2592000),
    -- How many free domains the pool should hold. Zero — the default, as for
    -- machines — means the hub buys none by itself.
    ADD COLUMN domain_pool_target int NOT NULL DEFAULT 0
        CHECK (domain_pool_target BETWEEN 0 AND 50);

-- A day never allows fewer orders than an hour does.
UPDATE entry_detection_policy SET fleet_orders_per_day = greatest(fleet_orders_per_day, fleet_orders_per_hour);
ALTER TABLE entry_detection_policy
    ADD CONSTRAINT entry_detection_policy_orders_day_covers_hour
        CHECK (fleet_orders_per_day >= fleet_orders_per_hour);

-- Since when a pool has held more live machines than it needs; NULL when it
-- does not. Written by the fleet pass, read by the surplus handback.
ALTER TABLE entry_fleet_targets ADD COLUMN surplus_since timestamptz;

-- One domain purchase for the pool at a time, as one scale_up (071).
CREATE UNIQUE INDEX entry_repairs_one_buy_domain
    ON entry_repairs (playbook)
    WHERE status = 'running' AND playbook = 'buy_domain';
