-- The РФ-каскад (hikkari-client plans/2026-11-01-rf-censorship-bypass-research.md
-- §2.4): a machine inside Russia in front of one XHTTP exit box. It carries no
-- relay of its own — nekko-edge in cascade mode hands its TCP to the exit —
-- so it is not an entry_boxes row: the pools, the autopilot and the rollouts
-- never see it. While a front is active, the exit's domain points at the front
-- and the exit's relays are signed with the front's address.
CREATE TABLE IF NOT EXISTS entry_fronts (
    name text PRIMARY KEY CHECK (name ~ '^[a-z]+[0-9]+$' AND length(name) <= 24),
    exit_box text NOT NULL REFERENCES entry_boxes(name) ON DELETE RESTRICT,
    address_ct bytea NOT NULL,
    provider text NOT NULL CHECK (provider <> ''),
    ssh_user text NOT NULL DEFAULT 'root' CHECK (ssh_user ~ '^[a-z_][a-z0-9_-]{0,31}$'),
    host_key text,
    edge_sha text CHECK (edge_sha ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    retired_at timestamptz
);

-- One active front per exit: the exit's records carry one address.
CREATE UNIQUE INDEX IF NOT EXISTS entry_fronts_one_per_exit ON entry_fronts (exit_box) WHERE retired_at IS NULL;
