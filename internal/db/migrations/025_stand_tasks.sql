-- The matrix stand on the hub's schedule (hikkari-client
-- plans/2026-11-04-tspu-detection-research.md, «Что делать дальше», 1).
-- Every interval the hub queues, for every active observation point
-- (entry_vantages) and every enabled knob, one run over every profile of the
-- stand's subscription; the point's prober claims it, measures it locally on
-- the core users run and sends the rows back into relay_runs.

-- What the stand measures. One row. subscription names the account whose
-- subscription is measured (hub_accounts.name): its profiles are the nodes
-- the stand sees. Empty means the stand is off.
CREATE TABLE IF NOT EXISTS stand_settings (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    subscription text NOT NULL DEFAULT '',
    interval_minutes integer NOT NULL DEFAULT 30 CHECK (interval_minutes BETWEEN 10 AND 1440),
    rounds integer NOT NULL DEFAULT 3 CHECK (rounds BETWEEN 1 AND 10),
    -- The page fetched with no protocol: the cover's own site behind the
    -- relay's address and port.
    baseline_path text NOT NULL DEFAULT '/',
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO stand_settings (id) VALUES (true) ON CONFLICT DO NOTHING;

-- The candidates of each knob: every value a run tries, one knob a run.
-- base sets other knobs on every value ("fp=chrome") when a knob shows a
-- difference only on top of another.
CREATE TABLE IF NOT EXISTS stand_knobs (
    knob text PRIMARY KEY CHECK (knob IN ('fp', 'sni', 'port', 'flow', 'alpn', 'burst')),
    vals text[] NOT NULL CHECK (cardinality(vals) BETWEEN 1 AND 8),
    base text NOT NULL DEFAULT '',
    enabled boolean NOT NULL DEFAULT true,
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO stand_knobs (knob, vals) VALUES ('fp', ARRAY['chrome', 'chrome_155', 'firefox', 'safari'])
ON CONFLICT DO NOTHING;

-- One run of one knob from one point. The plan is copied in, so a task says
-- what it measured even after the settings change.
CREATE TABLE IF NOT EXISTS stand_tasks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slot text NOT NULL,
    knob text NOT NULL,
    vals text[] NOT NULL,
    base text NOT NULL DEFAULT '',
    rounds integer NOT NULL,
    baseline_path text NOT NULL,
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'claimed', 'done', 'failed', 'expired')),
    error text NOT NULL DEFAULT '',
    core text NOT NULL DEFAULT '',
    rows_sent integer NOT NULL DEFAULT 0,
    rows_added integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    claimed_at timestamptz,
    finished_at timestamptz
);
CREATE INDEX IF NOT EXISTS stand_tasks_slot_knob_time ON stand_tasks (slot, knob, created_at DESC);
CREATE INDEX IF NOT EXISTS stand_tasks_open ON stand_tasks (status) WHERE status IN ('queued', 'claimed');
