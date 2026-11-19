-- Which cascade a capacity measurement was of. A cascade link carries no node
-- (subscription/cascade.go: its failures are the entry's and the trunk's as
-- much as the relay's), so relay_capacity_nodes never names it and the fleet
-- screen dropped its samples. The hub writes the pair, entry and exit box, when
-- it hands a capacity run the cascade's links (subscription.CapacityFleetLinks).
-- A cascade whose relay or knobs change gets a new key; the old pair stays and
-- keeps naming the history measured under it.
CREATE TABLE IF NOT EXISTS relay_capacity_cascades (
    config_key text PRIMARY KEY CHECK (config_key ~ '^[0-9a-f]{64}$'),
    entry_box text NOT NULL,
    exit_box text NOT NULL,
    seen_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS relay_capacity_cascades_pair ON relay_capacity_cascades(entry_box, exit_box);
