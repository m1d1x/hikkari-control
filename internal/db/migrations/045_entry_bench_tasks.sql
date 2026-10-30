-- 045 — the vantages benchmark a relay build (roadmap 42 П6.14 Ф3).
--
-- Before a new nekko-edge leaves its canary box, the RU vantage drives
-- messenger-shaped traffic through one of the box's relays to the hub — chat
-- exchanges, a voice-like stream, photo-sized transfers — once on the old
-- build and once on the new, and the rollout compares the two runs.
--
-- A bench is a task like an admission probe: the hub queues it, a vantage
-- claims it, runs it, reports back. Same table, a kind to tell them apart:
-- admission probes are claimed by the loop that must answer within the
-- provisioning job's patience, benches by one that may take ten minutes, and
-- neither waits behind the other.
--
--   params — what to run ({"durationSec": 300}); set by the hub.
--   result — the run's summary as the vantage measured it; what the rollout
--            compares. The same row goes to ClickHouse (entry_benches) as
--            the dataset; Postgres holds it because the decision must not
--            depend on analytics being up.

ALTER TABLE entry_probe_tasks
    ADD COLUMN kind text NOT NULL DEFAULT 'admission' CHECK (kind IN ('admission', 'bench')),
    ADD COLUMN params jsonb,
    ADD COLUMN result jsonb,
    -- A bench measures one path: one relay of the box (the binary is the box's).
    ADD CONSTRAINT entry_probe_tasks_bench_one_relay CHECK (kind <> 'bench' OR cardinality(relays) = 1),
    ADD CONSTRAINT entry_probe_tasks_bench_has_result
        CHECK (kind <> 'bench' OR (status = 'done') = (result IS NOT NULL));

DROP INDEX entry_probe_tasks_queued;
CREATE INDEX entry_probe_tasks_queued ON entry_probe_tasks (kind, created_at) WHERE status = 'queued';
