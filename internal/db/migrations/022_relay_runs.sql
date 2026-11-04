-- The relay dataset (hikkari-client/plans/2026-11-04-relay-search-method.md):
-- one row per measured profile per round, the way [diag:load] measures it —
-- 1 MB on one stream, 50 requests at once, 3 after. Rows come from the matrix
-- stand (source 'stand', vantage spb or lv) and from testers' load rounds
-- (source 'tester', vantage the install). Kept for good: the dataset is how a
-- knob that starts to be cut shows up against its own history.
--
-- params holds every knob of the run that is known (fp, sni, port, flow, alpn),
-- so a chart by any knob reads every row; knob names the one the stand varied,
-- 'baseline' for the same address with no protocol, '' for a tester's round.
-- No address and no secret is kept: a profile is named by its label.
CREATE TABLE IF NOT EXISTS relay_runs (
    id bigserial PRIMARY KEY,
    source text NOT NULL CHECK (source IN ('stand', 'tester')),
    -- What makes a row the same row when it is sent twice: the stand's
    -- vantage, round time, knob, value and round; a tester's install, line
    -- time and profile.
    source_key text NOT NULL,
    measured_at timestamptz NOT NULL,
    vantage text NOT NULL,
    network text NOT NULL DEFAULT '', -- the vantage's ISP as AS number, when known
    core text NOT NULL DEFAULT '',
    profile text NOT NULL,
    proto text NOT NULL DEFAULT '',
    knob text NOT NULL DEFAULT '',
    value text NOT NULL DEFAULT '',
    params jsonb NOT NULL DEFAULT '{}'::jsonb,
    round integer NOT NULL DEFAULT 0,
    dl bigint NOT NULL DEFAULT 0,
    dl_end text NOT NULL DEFAULT '',
    burst integer NOT NULL DEFAULT 0,
    burst_total integer NOT NULL DEFAULT 0,
    burst_timeout integer NOT NULL DEFAULT 0,
    after integer NOT NULL DEFAULT 0,
    after_total integer NOT NULL DEFAULT 0,
    load_error boolean NOT NULL DEFAULT false,
    received_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source, source_key)
);
CREATE INDEX IF NOT EXISTS relay_runs_profile_time ON relay_runs (profile, measured_at DESC);
CREATE INDEX IF NOT EXISTS relay_runs_time ON relay_runs (measured_at DESC);

-- How far the tester rounds have been read from support_tester_log_chunks.
CREATE TABLE IF NOT EXISTS relay_runs_cursor (
    name text PRIMARY KEY,
    last_chunk_id bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now()
);
