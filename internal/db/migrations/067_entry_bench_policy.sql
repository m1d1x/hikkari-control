-- 067 — the canary's thresholds move out of the code (plan 42 П6.14, owner's
-- decision 25.10).
--
-- `benchMinThroughputShare` (0.8) and `benchMaxRTTGrowth` (1.3) were Go
-- constants in internal/entry/bench.go, and how many pairs of runs a verdict
-- rests on was a hard-coded two (one pair, then an ABAB second pair when the
-- first failed on throughput alone). The stand showed why that cannot stay in
-- the code: throughput over the RU → AMS path swings by a factor of four
-- between runs of ONE unchanged binary (15.4 … 63.4 Mbit/s over seven runs),
-- and the rollout of 24cb2790 was rejected at 0.774 against a threshold of
-- 0.80 — a verdict about the hour, not about the build.
--
-- The answer is not to stop judging throughput: a build that halves the
-- throughput of an entry point is exactly what a canary exists to catch. The
-- answer is more samples. Averaging N pairs shrinks the spread of the mean by
-- about √N, and a longer run averages over more of the path's own drift, so
-- both knobs are here and both belong to the owner, who is the one looking at
-- the numbers when a rollout stalls.
--
-- One row and named columns, like entry_detection_policy (065), for the same
-- reasons: no parse and no validation at every read, and a typo cannot become
-- a silently missing threshold.
--
-- The defaults are NOT quite what the code shipped with, and deliberately so:
-- the thresholds are unchanged, and `pairs` goes from one to three. One pair
-- is the setting the stand proved cannot decide anything.

CREATE TABLE entry_bench_policy (
    -- One row, enforced by the type: `id` can only ever be true.
    id boolean PRIMARY KEY DEFAULT true CHECK (id),

    -- The share of the old build's throughput the new one must keep, in
    -- hundredths: 80 means the after runs may lose at most a fifth. Stored as
    -- an integer because a float knob in a console invites 0.800000001.
    min_throughput_pct int NOT NULL DEFAULT 80
        CHECK (min_throughput_pct BETWEEN 10 AND 100),

    -- How much the chat RTT p50 may grow, in hundredths: 130 is a third.
    max_rtt_growth_pct int NOT NULL DEFAULT 130
        CHECK (max_rtt_growth_pct BETWEEN 100 AND 300),

    -- How many before/after pairs one verdict rests on. The averaged pairs are
    -- what the comparison sees, so this is the knob that buys certainty with
    -- time: three pairs of five-minute runs is half an hour of benching for a
    -- verdict that no longer turns on one slow minute.
    pairs int NOT NULL DEFAULT 3
        CHECK (pairs BETWEEN 1 AND 10),

    -- How long one run lasts. The plan's window is 1–10 minutes; a run below a
    -- minute measures TCP ramp-up rather than the path.
    run_seconds int NOT NULL DEFAULT 300
        CHECK (run_seconds BETWEEN 60 AND 600),

    updated_at timestamptz NOT NULL DEFAULT now(),
    -- Who turned the knob last. Empty means "nobody yet — these are the
    -- defaults the code shipped with".
    updated_by text NOT NULL DEFAULT ''
);

-- The row exists from the start: a hub with no policy row would have to carry
-- a second copy of every default in Go, and the two would drift.
INSERT INTO entry_bench_policy (id) VALUES (true);
