-- An IPv6 order the hub has sent and whose address it has not seen yet
-- (hikkari-client plans/2026-11-05-fleet-ipv6-exit.md). The hoster's answer to an
-- order only says it is queued, and what its list shows for a queued order is not
-- known, so the hub keeps its own record: a second run waits instead of buying a
-- second address. Set before the order goes out, cleared when the address is seen
-- or the hoster refuses.
ALTER TABLE entry_boxes ADD COLUMN IF NOT EXISTS ipv6_ordered_at timestamptz;
