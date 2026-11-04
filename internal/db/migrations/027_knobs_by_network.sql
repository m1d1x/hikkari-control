-- A knob's value by the subscriber's network (hikkari-client
-- plans/2026-11-04-tspu-detection-research.md, «Что делать дальше», 3): what
-- the censor cuts differs by provider, so a node's value can differ by the
-- subscriber's ASN. entry_nodes keeps the value for everyone else.
--
-- The hub never learns a subscriber's address (caddy drops it before the hub):
-- the client resolves its own ASN from the database embedded in it
-- (hikkari-client engine/core/netinfo) and names it in the subscription
-- request. A point of the stand is ours, so its ASN is set by the operator.

-- The network a point of the stand measures from, as the dataset writes it
-- (AS12389). Its rows carry it, and the controller decides that network's
-- values on them. Unset: the point's rows count for the country only.
ALTER TABLE entry_vantages ADD COLUMN IF NOT EXISTS network text
  CHECK (network ~ '^AS[1-9][0-9]{0,9}$');

-- A node's knobs for subscribers of one ASN. A column left NULL is the node's
-- own value (entry_nodes). Written by the knob controller, or by hand.
CREATE TABLE IF NOT EXISTS entry_node_network_knobs (
    slug text NOT NULL REFERENCES entry_nodes(slug) ON DELETE CASCADE,
    asn bigint NOT NULL CHECK (asn > 0 AND asn < 4294967296),
    fingerprint text CHECK (fingerprint IN ('chrome', 'chrome_155', 'firefox', 'safari', 'edge', 'ios', 'android', 'randomized')),
    sni text CHECK (sni ~ '^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$'),
    alpn text CHECK (alpn IN ('h2', 'http/1.1', 'h2,http/1.1', 'http/1.1,h2')),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (slug, asn)
);

-- Which network a journal line is about; 0 is the node's own value.
-- network_red: the network's testers see the value red while no point of the
-- stand measures there, so no candidate can be chosen for it on evidence.
ALTER TABLE relay_knob_events ADD COLUMN IF NOT EXISTS asn bigint NOT NULL DEFAULT 0;
ALTER TABLE relay_knob_events DROP CONSTRAINT IF EXISTS relay_knob_events_kind_check;
ALTER TABLE relay_knob_events ADD CONSTRAINT relay_knob_events_kind_check
  CHECK (kind IN ('switch', 'all_red', 'address_cut', 'network_red'));
