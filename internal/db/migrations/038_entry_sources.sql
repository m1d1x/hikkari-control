-- 038 — the current signed source list (roadmap 42 П6.5).
--
-- entry_sources is the cold-start rendezvous: the signed SourceList (signed
-- OFFLINE with entry-1, nekko-entry) that tells a client with no working relay
-- where to fetch a fresh batch when the wire itself cannot be reached
-- (client-core/nekko-core net::coldstart). The client ships a baked seed
-- (net::entry::seed_sources); this table is how the operator replaces that seed
-- without a client release — the hub hands the newest list it holds in every
-- entry bundle, and the client adopts it when its issued_at is newer.
--
-- NOT sealed, unlike entry_relays. The only secret in the entry path is a relay
-- address (plan 42 §9: the value to a censor is the IP to ban), and that stays
-- under the server key. A source is a rendezvous, not an address: the same list
-- is baked in plaintext in the client binary and served over public DoH and
-- CDN. The entry-1 signature — not secrecy — is what a client trusts; the hub
-- stores the envelope byte for byte and cannot forge or alter it.
--
-- Singleton: there is exactly one current list. A re-import replaces it; an
-- older list never replaces a newer one (issued_at), enforced by the service.

CREATE TABLE entry_sources (
    -- One row, ever. The boolean PK pinned to true is the singleton.
    id          boolean PRIMARY KEY DEFAULT true CHECK (id = true),
    key_id      text NOT NULL,
    -- The signed envelope JSON (key_id/signature/payload), exactly as the
    -- signer wrote it and the client verifies it. Not sealed: see above.
    envelope    bytea NOT NULL,
    issued_at   timestamptz NOT NULL,
    expires_at  timestamptz NOT NULL,
    imported_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT entry_sources_lifetime CHECK (expires_at > issued_at)
);
