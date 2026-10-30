-- 077 — what "no evidence" means to the autopilot (audit 42 recheck,
-- second round of fixes).
--
-- ── An attempt that ended on no evidence is not a failure (НС1) ─────────
--
-- Owner's decision 27.10.
-- A fresh machine's admission that the vantage could not judge — every
-- answer probe_failed, or nobody claiming the task — used to end the repair
-- `failed`: the machine was handed back and deleted, and the attempt was a
-- strike, so a deaf prober handed the pool's top-up to a person with nothing
-- against any address. Such an attempt now ends `held`: the machine stays
-- rented for a live vantage, the incident reopens, and the strikes count only
-- `done` and `failed`. A held attempt still starts the cooldown.

ALTER TABLE entry_repairs DROP CONSTRAINT entry_repairs_status_check;
ALTER TABLE entry_repairs ADD CONSTRAINT entry_repairs_status_check
    CHECK (status IN ('running', 'done', 'failed', 'held'));

ALTER TABLE entry_repairs DROP CONSTRAINT entry_repairs_finished_has_time;
ALTER TABLE entry_repairs ADD CONSTRAINT entry_repairs_finished_has_time
    CHECK ((status IN ('done', 'failed', 'held')) = (finished_at IS NOT NULL));

-- ── A lost order is written off on evidence, not on one look (НС2) ───────
--
-- An order whose answer never came was released the first time one page of
-- the hoster's listing did not show its label. The machine may be on the next
-- page, late in the list, or renamed in the panel. Now each empty look is
-- counted, and the order leaves the budget only after several in a row over
-- time (entry.orderUnseenPasses / orderUnseenFor); a look that finds a tagged
-- machine the hub cannot account for resets the count.
ALTER TABLE entry_box_orders
    ADD COLUMN unseen_listings integer NOT NULL DEFAULT 0 CHECK (unseen_listings >= 0),
    ADD COLUMN unseen_since    timestamptz;
