-- 030 — the chat keys of E2E direct chats (pivot 76.2′ Р2, plan
-- 76-2-account-keys-pivot §3.3–§3.5). Replaces 023 (MLS KeyPackages) and 024
-- (MLS groups of devices), deleted with this migration: every database that
-- applied them was a test stand wiped for the pivot.
--
-- A DM's content is sealed under a chat key (CHK) that a participant's client
-- generates and seals (HPKE) once for each of the two ACCOUNTS — a grant per
-- account. Devices play no part: every device holding the account root opens
-- its account's grant. A chat key has a generation (1, 2, …) that moves only
-- when a participant rotates their root (76.5) — rarely.
--
--   * dm_key_heads — the chat's current generation. A new generation is
--     accepted only as current + 1 (compare-and-swap); the first one creates
--     the row, so two devices creating the chat's key at once race on the same
--     CAS and the loser takes the winner's key. A send locks this row FOR SHARE,
--     so an envelope commits in the generation it names.
--   * dm_keys — the grants, one per (chat, generation, account). An account
--     lists the grants addressed to it (a new device catching up on every
--     chat); `id` is that list's cursor.
--
-- The server reads a grant's clear header (version, generation, issuer) and
-- nothing else; the chat key inside is sealed to the recipient account's
-- exchange key, which only that account's root derives. What the server can
-- read here: that a chat has a key and when its generation moved — metadata
-- (plan 76 §2.5).
--
-- A grant is about the person it is addressed to: account erasure deletes
-- their grants (auth eraseSteps); the peer's stay, the history stays readable
-- to them. Heads go with the conversation.

CREATE TABLE dm_key_heads (
    conversation_id uuid PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE,
    generation      bigint NOT NULL CHECK (generation >= 1),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE dm_keys (
    -- The cursor of an account's grant list (ListDmKeys).
    id              bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    conversation_id uuid NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    generation      bigint NOT NULL CHECK (generation >= 1),
    -- The account the grant is sealed to.
    account_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    sealed          bytea NOT NULL CHECK (octet_length(sealed) BETWEEN 1 AND 4096),
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (conversation_id, generation, account_id)
);

CREATE INDEX dm_keys_by_account ON dm_keys (account_id, id);
