-- 046 — a rollout works box by box through jobs (roadmap 42 П6.14 Ф4).
--
-- The orchestrator does to a box what the console's «развернуть» does, plus
-- what a rollout needs around it, and the owner follows it in the same panel:
--
--   canary — the first box of a build: its relays leave the pool (testing),
--            a bench on the old build, the swap, an admission probe, a bench
--            on the new one, the comparison; back to the pool or rolled back.
--   update — every other box: drained, swapped, probed, back to the pool.
--
-- rollout_id ties those jobs to their rollout; a provisioning job has none.

ALTER TABLE entry_jobs
    DROP CONSTRAINT entry_jobs_kind_check,
    ADD CONSTRAINT entry_jobs_kind_check CHECK (kind IN ('provision', 'canary', 'update')),
    ADD COLUMN rollout_id uuid REFERENCES entry_rollouts (id) ON DELETE RESTRICT,
    ADD CONSTRAINT entry_jobs_rollout_kind CHECK ((kind = 'provision') = (rollout_id IS NULL));

CREATE INDEX entry_jobs_by_rollout ON entry_jobs (rollout_id, created_at) WHERE rollout_id IS NOT NULL;
