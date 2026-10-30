-- 060 — the hoster under key: a box the hub ordered itself, and what it costs
-- (roadmap 42 П9.2).
--
-- Until now a machine arrived by hand: the owner rented it in a provider's
-- panel and typed its address and root password into the console. That path
-- stays legal — a box with no provider_instance_id is one nobody can hand
-- back through an API, and the autopilot treats it as such: `replace_box` for
-- it orders a new machine from the default hoster and only wipes the old one.
--
-- What this migration adds is the other path. The hub orders the machine
-- itself through entry.BoxProvider, so three things have to be recorded:
--
--   provider_instance_id — the hoster's own id for the instance. It is what
--     reboot and delete name, and it is the only link between a row here and
--     a machine that costs money there. UNIQUE where present: two boxes
--     pointing at one instance would mean deleting the first takes the second
--     down with it.
--   provider_region — the hoster's own region id ("ams"). The box already
--     records a human location ("Amsterdam, NL"), which is for reading; a
--     replacement has to be ORDERED in the same place, and parsing a city
--     name back into a region id would be guessing at the hoster's ids.
--   plan, monthly_cents — the rental, as the hoster quoted it at order time.
--     The price is stored and not re-read, because the figure the console
--     shows is what we are being charged for THIS box, which a later price
--     change does not alter retroactively. Until this column existed the
--     dashboard's `money.machines` was absent and said so in `missing`
--     (П9.9); it is computable from here on.
--
-- The hoster's own defaults live on its provider row: which region and which
-- plan an order takes when nobody says. They belong with the credentials and
-- the caps (migration 056) rather than in the environment, for the same
-- reason the caps did — a policy the owner edits is not a redeploy.

ALTER TABLE entry_boxes
    ADD COLUMN provider_instance_id text,
    ADD COLUMN provider_region      text NOT NULL DEFAULT '',
    ADD COLUMN plan                 text NOT NULL DEFAULT '',
    ADD COLUMN monthly_cents        integer CHECK (monthly_cents IS NULL OR monthly_cents >= 0);

CREATE UNIQUE INDEX entry_boxes_one_box_per_instance
    ON entry_boxes (provider_instance_id)
    WHERE provider_instance_id IS NOT NULL;

ALTER TABLE entry_providers
    ADD COLUMN default_region text NOT NULL DEFAULT '',
    ADD COLUMN default_plan   text NOT NULL DEFAULT '';

-- `reboot` is a job like any other: one per box at a time, its steps and its
-- live log in the console. It touches the machine through the hoster's API
-- rather than over ssh, and then waits for ssh to come back — which is the
-- part worth watching, and the reason it is a job and not a bare API call.
ALTER TABLE entry_jobs
    DROP CONSTRAINT entry_jobs_kind_check,
    DROP CONSTRAINT entry_jobs_rollout_kind,
    ADD CONSTRAINT entry_jobs_kind_check
        CHECK (kind IN ('provision', 'canary', 'update', 'retire', 'rekey', 'reboot')),
    ADD CONSTRAINT entry_jobs_rollout_kind
        CHECK ((kind IN ('provision', 'retire', 'rekey', 'reboot')) = (rollout_id IS NULL));
