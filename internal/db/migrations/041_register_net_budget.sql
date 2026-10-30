-- 041 — successful registrations per network (Sybil brake).
--
-- Every per-account limit (the device-link pause, the email-code quota) is
-- only as strong as an account is expensive, and an account cost a confirmed
-- email and a 20-bit proof of work: with a catch-all domain, some 600 accounts
-- an hour from one address. The gateway's pre-auth guard counts only FAILED
-- attempts, so nothing counted the ones that worked.
--
-- This table counts accounts created per network (netkey.Of: the IPv4
-- address, the IPv6 /64) over a fixed window, and registration from a network
-- whose window is full is refused (auth.MaxRegistrationsPerNet). The count is
-- taken in the transaction that inserts the user, so only a committed account
-- spends it and two concurrent registrations cannot both take the last one.
--
-- As in e2e_link_net_lockouts (040), the network is not kept in the clear:
-- net_tag is an HMAC of it under a key derived from the server's content key
-- (netkey.Tag), with a label of this limit's own.

CREATE TABLE auth_register_net_budget (
    net_tag        bytea PRIMARY KEY CHECK (octet_length(net_tag) = 32),
    window_started timestamptz NOT NULL,
    registrations  integer NOT NULL CHECK (registrations > 0)
);
