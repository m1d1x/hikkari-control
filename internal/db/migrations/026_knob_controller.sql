-- The knob controller (hikkari-client plans/2026-11-04-tspu-detection-research.md,
-- «Что делать дальше», 2): every few minutes the hub reads the stand's rows
-- and moves a node off a knob value Russia cuts onto a candidate that passes,
-- before anyone complains. Candidates are the stand's own (stand_knobs): what
-- is measured is what may be chosen.

-- The controller's policy, beside the stand's plan. Shares are of the burst
-- (50 requests at once) over the window, from points outside control_slots.
ALTER TABLE stand_settings
    ADD COLUMN IF NOT EXISTS controller text NOT NULL DEFAULT 'auto' CHECK (controller IN ('auto', 'off')),
    ADD COLUMN IF NOT EXISTS knob_window_minutes integer NOT NULL DEFAULT 90 CHECK (knob_window_minutes BETWEEN 30 AND 1440),
    ADD COLUMN IF NOT EXISTS knob_min_rounds integer NOT NULL DEFAULT 6 CHECK (knob_min_rounds BETWEEN 1 AND 100),
    ADD COLUMN IF NOT EXISTS knob_low real NOT NULL DEFAULT 0.85 CHECK (knob_low > 0 AND knob_low < 1),
    ADD COLUMN IF NOT EXISTS knob_high real NOT NULL DEFAULT 0.95 CHECK (knob_high > 0 AND knob_high <= 1),
    ADD COLUMN IF NOT EXISTS knob_margin real NOT NULL DEFAULT 0.10 CHECK (knob_margin >= 0 AND knob_margin < 1),
    ADD COLUMN IF NOT EXISTS knob_cooldown_minutes integer NOT NULL DEFAULT 60 CHECK (knob_cooldown_minutes BETWEEN 0 AND 1440),
    -- Points outside Russia: they tell a value the server refuses (edge, qq
    -- on ams3) from one the censor cuts, and never decide a switch alone.
    ADD COLUMN IF NOT EXISTS control_slots text[] NOT NULL DEFAULT '{lv-main}';

-- What the controller did and why: a switch, or a state it could not act on
-- (every candidate red; the address itself cut). Drawn on the knob's chart.
-- profile is the node's label, as the dataset's rows name it.
CREATE TABLE IF NOT EXISTS relay_knob_events (
    id bigserial PRIMARY KEY,
    at timestamptz NOT NULL DEFAULT now(),
    node text NOT NULL,
    profile text NOT NULL,
    knob text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('switch', 'all_red', 'address_cut')),
    from_value text NOT NULL DEFAULT '',
    to_value text NOT NULL DEFAULT '',
    reason text NOT NULL DEFAULT '',
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS relay_knob_events_profile_time ON relay_knob_events (profile, knob, at DESC);
CREATE INDEX IF NOT EXISTS relay_knob_events_node_time ON relay_knob_events (node, knob, at DESC);
