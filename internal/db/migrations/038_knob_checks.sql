-- Knob checks on the relay dataset, in place of testers' wire mirror
-- (hikkari-client plans/2026-11-07-knob-truth-wire-diagnostics.md, section 12).
--
-- 032 read [diag:wire] lines: the ClientHello each tester's core sent to a
-- loopback mirror, held to a reference per core revision. A hello is set by
-- the core's revision and the config alone, so the same binary with the same
-- config sends it on every machine: the release's test pins it, and the
-- testers' copy added nothing. What stays is whether the config holds what
-- the hub handed out; the load rounds in relay_runs carry those knobs already.
-- entry_nodes.knobs_at and its triggers (032) stay: the checks wait for them.

DROP TABLE IF EXISTS wire_hellos;
DROP TABLE IF EXISTS wire_reference;
DELETE FROM relay_runs_cursor WHERE name = 'tester_wire';

-- One row per tester round checked (relay_runs, source 'tester'): a round is
-- checked once, against the knobs in force when it was measured.
CREATE TABLE IF NOT EXISTS knob_checks (
    run_id bigint PRIMARY KEY REFERENCES relay_runs (id) ON DELETE CASCADE,
    state text NOT NULL CHECK (state IN ('clean', 'mismatch', 'unknown', 'settling')),
    -- What the hub handed out, as the link writes it: {fp, sni, alpn}; {} when unknown.
    declared jsonb NOT NULL DEFAULT '{}',
    -- [{code, knob, want, got, msg}]; [] unless state is 'mismatch'.
    findings jsonb NOT NULL DEFAULT '[]',
    checked_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS knob_checks_mismatch ON knob_checks (checked_at DESC) WHERE state = 'mismatch';
-- The checker's pass looks for tester rounds of the last days without a check.
CREATE INDEX IF NOT EXISTS relay_runs_tester_time ON relay_runs (measured_at DESC) WHERE source = 'tester';
