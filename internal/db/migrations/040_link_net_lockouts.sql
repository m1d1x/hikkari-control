-- 040 — the device-link brake per network, beside the one per account.
--
-- e2e_link_lockouts pauses a typing ACCOUNT after five failed links in a row.
-- Accounts are cheap to make, so a hundred of them on one network are five
-- hundred attempts an hour: enough to keep landing on other people's codes,
-- holding their sign-in screens («это устройство уже подключается») and
-- burning their links. This table counts the same failures by the network
-- the typing device connected from (netkey.Of: the IPv4 address, the IPv6
-- /64), so the pause is one per network however many accounts it brings.
--
-- The network is metadata and is not kept in the clear: net_tag is an HMAC of
-- it under a key derived from the server's content key (cryptobox.Derive), so
-- a dump holds tags that name no address. Rotating the content key changes
-- every tag, which here only forgets running counts.
--
-- Unlike the account's run, a success does not reset it: a network that could
-- clear its count by linking its own second device would never be paused.
-- The count is a window instead — failures since window_started.

CREATE TABLE e2e_link_net_lockouts (
    net_tag        bytea PRIMARY KEY CHECK (octet_length(net_tag) = 32),
    window_started timestamptz NOT NULL,
    failures       integer NOT NULL,
    paused_until   timestamptz
);

-- The joining device's network, taken at join: failures reported later (a
-- wrong secret, the new device refusing the account) count against the
-- network that typed the code, not the one reporting them.
ALTER TABLE e2e_device_links ADD COLUMN live_net_tag bytea;
