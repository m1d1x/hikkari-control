-- Which logical node a capacity measurement was of. relay_capacity keys a
-- sample by its transport configuration (capacity.Key: the link without the
-- credential and the node's name), so on its own it cannot say which profile
-- of the fleet screen it belongs to. The hub writes the pair when it hands a
-- capacity run its links (subscription.CapacityFleetLinks), where both are on
-- the same link. A node whose knobs change gets a new key; the old pair stays
-- and keeps naming the history measured under it.
CREATE TABLE IF NOT EXISTS relay_capacity_nodes (
    config_key text PRIMARY KEY CHECK (config_key ~ '^[0-9a-f]{64}$'),
    node_slug text NOT NULL,
    seen_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS relay_capacity_nodes_slug ON relay_capacity_nodes(node_slug);
