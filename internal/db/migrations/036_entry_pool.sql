-- 036 — the entry pool and the grant registry (roadmap 42 П6.2).
--
-- entry_relays is the pool of relay records signed OFFLINE with entry-1
-- (client-core/nekko-entry). The hub cannot make records, only hand out the
-- ones it holds. The signed envelope is the address — and the address is the
-- secret (plan 42 §9: the value to a censor is the IP to ban) — so it is kept
-- under the server key (cryptobox); only what the hub decides by stays
-- readable: name, kind, status, lifetime.
--
-- entry_grants is who was handed which relay and when: the batch an account
-- dials (active grants), and the dataset for the abuse model later (П6.7) —
-- a ban becomes a labelled example only if the exposure is on record. The
-- per-grant observations (IP, provider, client, account age) are sealed in
-- meta_ct; account erasure clears them and keeps the bare row (relay, times),
-- which says nothing about the person.
--
-- (035 is taken by the unmerged claude/entry-without-password branch.)

CREATE TABLE entry_relays (
    id          bigserial PRIMARY KEY,
    -- The record's own name (nekko-entry: [a-z0-9-]{1,32}). A re-signed record
    -- replaces the envelope under the same name; a replacement relay gets a
    -- new one.
    name        text NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9-]{1,32}$'),
    -- bootstrap: reachable through public sources, no account (П6.5).
    -- personal: handed out only in account batches.
    kind        text NOT NULL CHECK (kind IN ('bootstrap', 'personal')),
    status      text NOT NULL DEFAULT 'alive' CHECK (status IN ('alive', 'dead')),
    key_id      text NOT NULL,
    -- cryptobox(envelope JSON, exactly as signed and stored by the signer).
    envelope_ct bytea NOT NULL,
    issued_at   timestamptz NOT NULL,
    expires_at  timestamptz NOT NULL,
    imported_at timestamptz NOT NULL DEFAULT now(),
    died_at     timestamptz,
    CONSTRAINT entry_relays_dead_has_time CHECK ((status = 'dead') = (died_at IS NOT NULL)),
    CONSTRAINT entry_relays_lifetime CHECK (expires_at > issued_at)
);

CREATE TABLE entry_grants (
    id            bigserial PRIMARY KEY,
    -- RESTRICT: accounts are tombstoned, never deleted; a grant outlives its
    -- account's erasure as a bare row.
    account_id    uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    relay_id      bigint NOT NULL REFERENCES entry_relays(id) ON DELETE RESTRICT,
    -- initial: the account's first batch. death_replacement: a slot refilled
    -- after its relay died — the only other way a grant is ever made.
    trigger       text NOT NULL CHECK (trigger IN ('initial', 'death_replacement')),
    granted_at    timestamptz NOT NULL DEFAULT now(),
    revoked_at    timestamptz,
    revoke_reason text CHECK (revoke_reason IN ('relay_dead')),
    -- cryptobox(JSON of what was observed at grant time); NULL after erasure.
    meta_ct       bytea,
    CONSTRAINT entry_grants_revoked_has_reason CHECK ((revoked_at IS NULL) = (revoke_reason IS NULL))
);

-- One live grant per (account, relay); the batch query reads these.
CREATE UNIQUE INDEX entry_grants_active
    ON entry_grants (account_id, relay_id)
    WHERE revoked_at IS NULL;

-- A relay's death revokes every live grant of it.
CREATE INDEX entry_grants_active_by_relay
    ON entry_grants (relay_id)
    WHERE revoked_at IS NULL;
