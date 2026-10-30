-- 065 — the eviction thresholds move out of the code (plan 42 К3).
--
-- How many failed probes in a row evict a point, how many bans of one cause
-- across the fleet count as a wave, and over what window, were Go constants:
-- `verdictWindow` in detector.go, `repairStrikes` / `waveWindow` / `waveLimit`
-- in repair.go. They are not constants in the sense that word implies — they
-- are the knobs the owner turns while looking at the ban chart, which means
-- turning one meant a rebuild and a deploy. The console's detection screen
-- turns them now, and that screen reads this table.
--
-- One row, named columns, with the checks the handler would otherwise have to
-- repeat. A key/value table would have cost a parse and a validation at every
-- read, and let a typo become a silently missing threshold.
--
-- The defaults ARE the values the code shipped with, so running this migration
-- changes no behaviour at all: 3 strikes, a wave of 3 causes in an hour, a
-- verdict believed for 3 probe rounds, 15 minutes of cooldown after a repair.
--
-- Two of the five are not numbers:
--
--   require_two_vantages — a ban seen from one vantage is a regional block,
--     not a fleet-wide one, so confirming it from a second observation point
--     is the right rule. It defaults to FALSE because the grid has exactly one
--     RU vantage: turning it on today would confirm nothing and therefore
--     evict nothing. The handler refuses to set it while the grid is that
--     small, rather than storing a flag that silently stops every eviction.
--
--   ignore_network_down — the vantage's own outage is not evidence about a
--     machine (diagnose.go reads `every("network_down")` as CauseBlind before
--     any other rule). It is here as a stored invariant rather than a knob:
--     its off position would retire healthy machines during our own network
--     trouble, so the handler refuses to clear it and the screen shows it
--     locked with that reason.

CREATE TABLE entry_detection_policy (
    -- One row, enforced by the type: `id` can only ever be true.
    id boolean PRIMARY KEY DEFAULT true CHECK (id),

    -- How many probe rounds in a row must say "banned" before the hub stops
    -- repairing a (box, cause) and hands it to a person.
    fail_strikes int NOT NULL DEFAULT 3
        CHECK (fail_strikes BETWEEN 1 AND 10),

    -- This many machines broken by one cause inside the window and the wave
    -- fuse blows: the hub stops acting on that cause until a person lifts it.
    wave_limit int NOT NULL DEFAULT 3
        CHECK (wave_limit BETWEEN 1 AND 20),
    wave_window_seconds int NOT NULL DEFAULT 3600
        CHECK (wave_window_seconds BETWEEN 60 AND 86400),

    -- How far back a probe verdict is believed. Below one probe round the hub
    -- would go blind between rounds; far above it a stale verdict holds an
    -- incident open after the box recovered.
    verdict_window_seconds int NOT NULL DEFAULT 180
        CHECK (verdict_window_seconds BETWEEN 60 AND 3600),

    -- The gap after an attempt on the same cause, so a repair that did not
    -- take does not become a loop.
    repair_cooldown_seconds int NOT NULL DEFAULT 900
        CHECK (repair_cooldown_seconds BETWEEN 60 AND 86400),

    require_two_vantages boolean NOT NULL DEFAULT false,
    ignore_network_down boolean NOT NULL DEFAULT true
        CHECK (ignore_network_down),

    updated_at timestamptz NOT NULL DEFAULT now(),
    -- Who turned the knob last. Empty means "nobody yet — these are the
    -- defaults the code shipped with".
    updated_by text NOT NULL DEFAULT ''
);

-- The row exists from the start: a hub with no policy row would have to carry
-- a second copy of every default in Go, and the two would drift.
INSERT INTO entry_detection_policy (id) VALUES (true);
