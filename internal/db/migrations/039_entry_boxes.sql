-- 039 — the machines behind the entry relays (roadmap 42 П6.6, operator view).
--
-- A box is one rented machine with one public address: the unit a censor bans,
-- so the unit that fails. Relays on it are named <box>-<n> (ams1-1, ams1-2) by
-- the signer (client-core/nekko-entry registry.rs), which also writes
-- inventory.json — this table is that file, imported by the operator.
--
-- Everything here is for the admin console only; nothing in it is ever handed
-- to a client. The address is the secret (plan 42 §9), so it is sealed under
-- the server key exactly like entry_relays.envelope_ct and opened only when the
-- owner looks. Provider, location and rental are plain: they say nothing a
-- censor could act on.
--
-- Relays are not linked by a foreign key. A relay's address, port and protocol
-- live in its signed record and are read from there; the console groups relays
-- under a box by that address. A box is never deleted: a rented-out machine
-- stays on record next to the dead relays that ran on it.

CREATE TABLE entry_boxes (
    name         text PRIMARY KEY CHECK (name ~ '^[a-z]+[0-9]+$' AND length(name) <= 24),
    -- cryptobox(address) — the same seal as entry_relays.envelope_ct.
    address_ct   bytea NOT NULL,
    provider     text NOT NULL CHECK (provider <> ''),
    location     text NOT NULL CHECK (location <> ''),
    rented_until date,
    imported_at  timestamptz NOT NULL DEFAULT now()
);
