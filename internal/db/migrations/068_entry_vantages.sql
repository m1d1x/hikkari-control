-- 068 — observation points become a table (plan 42 К3′ / П9.12).
--
-- An observation point (vantage) used to be nothing more than the string in
-- ENTRY_PROBE_SOURCE_REGION on the box running cmd/entry-prober, arriving in
-- the source_region column of ClickHouse's entry_probes. Postgres knew nothing
-- about it: if a prober went quiet for longer than the chart window, its
-- region simply vanished from the query and the console could not tell "this
-- slot is silent" from "this slot never existed". Nor could it say which
-- machine sits in the slot, which provider carries it, or replace that machine
-- from the screen.
--
-- A row here is a named slot (`ru-msk`, `ru-spb`) with a machine inside it.
-- The slot key is the same `[a-z0-9-]{1,32}` token that cmd/entry-prober sends
-- as sourceRegion, so joining the slot list against entry_probes needs no
-- translation table.
--
-- The machine inside a slot can be pointed at a fleet box (`box` referencing
-- entry_boxes) OR hold its own sealed host (`host_ct` encrypted through
-- cryptobox, because a domestic prober VPS does not have to be an entry relay
-- box). Plaintext IP addresses are never stored here for the same reason
-- entry_boxes and entry_relays seal theirs.

CREATE TABLE IF NOT EXISTS entry_vantages (
    slot text PRIMARY KEY
        CHECK (slot ~ '^[a-z0-9-]{1,32}$'),

    -- Human label shown alongside the slot key on the prober screen.
    title text NOT NULL DEFAULT '',

    -- Optional link when the slot is served by a box registered in entry_boxes.
    box text REFERENCES entry_boxes(name) ON DELETE SET NULL,

    -- cryptobox.Encrypt(host/IP). NULL when the slot has no standalone host
    -- (either empty or taking its address from `box`).
    host_ct bytea,

    -- Provider or ISP carrying the observation machine.
    provider text NOT NULL DEFAULT '',

    status text NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'retired')),

    created_at timestamptz NOT NULL DEFAULT now(),
    retired_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by text NOT NULL DEFAULT '',

    CONSTRAINT entry_vantages_retired_check
        CHECK ((status = 'retired') = (retired_at IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS idx_entry_vantages_status ON entry_vantages (status);

-- Deploying nekko-entry-prober to a slot's box runs as a tracked job on that box.
ALTER TABLE entry_jobs
    DROP CONSTRAINT entry_jobs_kind_check,
    DROP CONSTRAINT entry_jobs_rollout_kind,
    ADD CONSTRAINT entry_jobs_kind_check
        CHECK (kind IN ('provision', 'canary', 'update', 'retire', 'rekey', 'reboot',
                        'restart', 'redest', 'prober')),
    ADD CONSTRAINT entry_jobs_rollout_kind
        CHECK ((kind IN ('provision', 'retire', 'rekey', 'reboot', 'restart', 'redest', 'prober'))
               = (rollout_id IS NULL));
