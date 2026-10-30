-- A logical VPN node outlives its relay and rented machine. Replacements
-- reuse an unavailable slot in the same protocol/region; IPs stay sealed.
CREATE TABLE entry_nodes (
    slug text PRIMARY KEY CHECK (slug ~ '^[a-z0-9-]{1,32}$'),
    relay_id bigint NOT NULL UNIQUE REFERENCES entry_relays(id) ON DELETE RESTRICT,
    proto text NOT NULL,
    region text NOT NULL DEFAULT '',
    bound_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX entry_nodes_pool ON entry_nodes (proto, region);
