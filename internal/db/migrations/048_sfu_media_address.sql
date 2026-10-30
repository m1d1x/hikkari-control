-- 048 — a shard announces where its media arrives (voice plan Ф3).
--
-- A voice entry (a Hysteria2 relay in front of one region, roadmap 42 voice
-- branch) forwards datagrams to its region's SFUs and nowhere else — it is not
-- an open UDP proxy. The hub renders that allowlist when it deploys the
-- entry, and needs every shard's media address to do it: the public IP Pion
-- pins its host candidate to (SFU_PUBLIC_IP) and the UDP range it binds
-- (SFU_UDP_MIN..SFU_UDP_MAX). public_url does not say: it names the signalling
-- host, which may sit behind a proxy on another address.
--
-- Empty / zero = the shard pins neither (a dev shard on every interface); no
-- voice entry can forward to it.

ALTER TABLE sfu_nodes
    ADD COLUMN media_ip text NOT NULL DEFAULT '',
    ADD COLUMN media_udp_min integer NOT NULL DEFAULT 0,
    ADD COLUMN media_udp_max integer NOT NULL DEFAULT 0,
    ADD CONSTRAINT sfu_nodes_media_udp_range CHECK (
        (media_udp_min = 0 AND media_udp_max = 0)
        OR (media_udp_min BETWEEN 1 AND 65535 AND media_udp_max BETWEEN media_udp_min AND 65535));
