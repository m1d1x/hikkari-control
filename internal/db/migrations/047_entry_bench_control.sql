-- 047 — a canary bench runs beside a control (roadmap 42 П6.14 Ф4, owner's
-- decision 19.10).
--
-- The stand's first two live canaries failed on a binary identical to the one
-- they replaced: the RU → AMS path drifted between the before and the after
-- run (routine probe RTT 340 → 600–2700 ms one evening), and a before/after
-- comparison on one box cannot tell a slower build from a slower hour. With a
-- second box of the architecture, the vantage benches the canary and a
-- control box still on the old build AT ONCE, before and after, and the build
-- is judged on how the canary moved against the control — drift on the shared
-- path cancels out.
--
-- So a bench task names one relay (a lone box) or two: the canary's, then the
-- control's. The result carries the control's run beside the canary's.

ALTER TABLE entry_probe_tasks
    DROP CONSTRAINT entry_probe_tasks_bench_one_relay,
    ADD CONSTRAINT entry_probe_tasks_bench_relays
        CHECK (kind <> 'bench' OR cardinality(relays) BETWEEN 1 AND 2);
