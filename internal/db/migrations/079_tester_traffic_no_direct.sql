-- What went past the VPN is the tester's own business, not our service (owner,
-- 18.11: «трафик мимо VPN это личное дело тестера»): the hub keeps a tester's
-- bytes through the tunnel only. Dropping the columns also erases what client
-- 1.0.93 sent of the bytes past the tunnel (the owner agreed). Client 1.0.94
-- writes "[downtime] bytes from=<unix> v=tu,td;.;-"; the hub drops the last two
-- figures of a 1.0.93 line.
ALTER TABLE product_tester_traffic_minutes
    DROP COLUMN IF EXISTS direct_up,
    DROP COLUMN IF EXISTS direct_down;
