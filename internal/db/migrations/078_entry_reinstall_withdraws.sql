-- 078 — a sentence passed on a machine in the operator's hands is withdrawn
-- when the operator lets go (audit 42 third pass, НН-3-1; owner's decision
-- 27.10).
--
-- The operator forgets a machine's host key and reinstalls it. Meanwhile its
-- relays are unreachable, the vantage reads ip_banned, the machine is evicted
-- and its second chance may die. The write-off waits while the key is
-- forgotten (boxInOperatorsHands) — and carried the sentence out on the first
-- pass after the operator's job took the new key: the machine just
-- reinstalled was handed back and deleted at the hoster on evidence gathered
-- against the machine before it.
--
-- Now the operator's job meeting the new key withdraws the machine's open or
-- dead second chance (status 'withdrawn', ended), and its suspect relays go
-- back to `pending`, admitted again on that deploy's own fresh verdict. A new
-- second chance opens only on a new eviction: the unique index on 'dead'
-- no longer holds a withdrawn row, so "death is once" is once per sentence
-- the machine is still under.

ALTER TABLE entry_suspects DROP CONSTRAINT entry_suspects_status_check;
ALTER TABLE entry_suspects ADD CONSTRAINT entry_suspects_status_check
    CHECK (status IN ('suspect', 'returned', 'dead', 'withdrawn'));
