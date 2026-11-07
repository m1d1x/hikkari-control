-- Dedicated measurement peers: separate from admission probes and subscribers.
-- Shared address sequence prevents collisions with subscriber tunnel addresses.
CREATE TABLE relay_capacity_awg_peers (
 slot text PRIMARY KEY REFERENCES entry_vantages(slot) ON DELETE CASCADE,
 private_key text NOT NULL UNIQUE,
 public_key text NOT NULL UNIQUE,
 address inet NOT NULL UNIQUE,
 created_at timestamptz NOT NULL DEFAULT now()
);
