-- 054 — closed rooms on E2E (roadmap 76.4.2, plan 76 §1.44–§1.46).
--
-- A closed room is a standalone room (group_channels without server_id) with
-- is_public = false — fixed at creation, never toggled (§1.45). Its content is
-- sealed under a room key the members' clients generate, one key per
-- GENERATION, handed to each member account by a GRANT (HPKE, like a DM's,
-- migration 030). Its membership is a LOG of records, each signed by the
-- member who made the change and chained to the one before by hash
-- (nekko-e2e room.rs). The server checks no signature — it holds no key to
-- trust them against — but it holds the chain and applies each record to its
-- own roster, and it accepts a membership change only together with the
-- record that states it.
--
--   * room_heads — one row per closed room: the head of its log (sequence
--     number and hash, both computed by the server), «history for newcomers»
--     as the log last set it, the seq of the last removal or departure, and
--     the room's current key generation with the head it was minted at. A log
--     append locks it FOR UPDATE; a send locks it FOR SHARE, so an envelope
--     commits in the generation it names. generation = 0: no key yet.
--     A generation minted before the last exit (key_seq < exit_seq) still
--     names the member who left, and a send under it is refused as stale: the
--     next sender mints the room's next generation (plan 76.4, lazy rotation).
--   * room_log — the records, in order.
--   * room_generations — one descriptor per generation (the head it was
--     minted at, the roster with each member's signing key, the minter's
--     signature; ~70 KiB at 500 members), with the digest every grant names.
--   * room_keys — the grants, one per (room, generation, account). An account
--     lists the grants addressed to it (a new device catching up); `id` is
--     that list's cursor.
--
-- What the server can read here is membership and its order — metadata it
-- has anyway (plan 76 §2.5). The room's name, description and avatar are
-- content (§2.5): a closed room keeps them as ONE envelope, `profile_envelope`,
-- sealed under a generation of the room key like a message; the name column
-- is empty, the description too, and a closed room has no public address
-- (slug). The avatar's storage key stays in the clear beside the envelope,
-- like a message's file pointer (§1.28): the server hands the file only to the
-- room's members, by that key. The picture itself is a sealed file (76.3).
--
-- The room's messages live in channel_messages like any room's; a closed
-- room's are envelopes — the same columns and the same CHECK as an E2E direct
-- chat's (migrations 025, 026, 034). Their reactions are sealed sets like a
-- DM's (027), in their own table: e2e_reactions references messages.
--
-- Data: the stand holds test data only (owner decision, plan 76 §10). A
-- standalone private room created before this migration has a readable name
-- and no log; it is deleted, with its handle.

DELETE FROM handles h
 USING group_channels gc
 WHERE h.owner_type = 'group_channel' AND h.owner_id = gc.id
   AND gc.server_id IS NULL AND NOT gc.is_public;
DELETE FROM group_channels WHERE server_id IS NULL AND NOT is_public;

ALTER TABLE group_channels ALTER COLUMN slug DROP NOT NULL;
ALTER TABLE group_channels
    ADD COLUMN profile_envelope bytea,
    ADD COLUMN profile_epoch    bigint,
    ADD COLUMN profile_revision integer;

ALTER TABLE group_channels ADD CONSTRAINT group_channels_profile_whole CHECK (
    (profile_envelope IS NULL AND profile_epoch IS NULL AND profile_revision IS NULL)
    OR (profile_envelope IS NOT NULL AND profile_epoch >= 1 AND profile_revision >= 0
        AND octet_length(profile_envelope) BETWEEN 1 AND 65536)
);

-- A closed room says nothing readable about itself, and only it has a profile
-- envelope. Every other room has a public address.
ALTER TABLE group_channels ADD CONSTRAINT group_channels_closed_is_sealed CHECK (
    CASE WHEN server_id IS NULL AND NOT is_public
        THEN name = '' AND description = '' AND slug IS NULL
             AND NOT is_broadcast AND NOT is_discussion AND NOT is_synthetic
             AND banner_s3_key IS NULL
        ELSE slug IS NOT NULL AND profile_envelope IS NULL
    END
);

CREATE TABLE room_heads (
    room_id       uuid PRIMARY KEY REFERENCES group_channels(id) ON DELETE CASCADE,
    log_seq       bigint NOT NULL CHECK (log_seq >= 0),
    log_hash      bytea NOT NULL CHECK (octet_length(log_hash) = 32),
    share_history boolean NOT NULL,
    exit_seq      bigint NOT NULL DEFAULT 0 CHECK (exit_seq >= 0 AND exit_seq <= log_seq),
    generation    bigint NOT NULL DEFAULT 0 CHECK (generation >= 0),
    key_seq       bigint CHECK (key_seq >= 0 AND key_seq <= log_seq),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CHECK ((generation = 0) = (key_seq IS NULL))
);

CREATE TABLE room_log (
    room_id    uuid NOT NULL REFERENCES group_channels(id) ON DELETE CASCADE,
    seq        bigint NOT NULL CHECK (seq >= 0),
    record     bytea NOT NULL CHECK (octet_length(record) BETWEEN 1 AND 8192),
    hash       bytea NOT NULL CHECK (octet_length(hash) = 32),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (room_id, seq)
);

CREATE TABLE room_generations (
    room_id    uuid NOT NULL REFERENCES group_channels(id) ON DELETE CASCADE,
    generation bigint NOT NULL CHECK (generation >= 1),
    descriptor bytea NOT NULL CHECK (octet_length(descriptor) BETWEEN 1 AND 98304),
    digest     bytea NOT NULL CHECK (octet_length(digest) = 32),
    key_seq    bigint NOT NULL CHECK (key_seq >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (room_id, generation)
);

CREATE TABLE room_keys (
    -- The cursor of an account's grant list (ListRoomKeys).
    id         bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    room_id    uuid NOT NULL,
    generation bigint NOT NULL,
    -- The account the grant is sealed to.
    account_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- `sealed`, not `grant`: GRANT is reserved in Postgres.
    sealed     bytea NOT NULL CHECK (octet_length(sealed) BETWEEN 1 AND 2048),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (room_id, generation, account_id),
    FOREIGN KEY (room_id, generation) REFERENCES room_generations (room_id, generation) ON DELETE CASCADE
);

CREATE INDEX room_keys_by_account ON room_keys (account_id, id);

-- The messages of a closed room: the E2E columns of messages (025, 034).
ALTER TABLE channel_messages
    ADD COLUMN e2e_envelope bytea,
    ADD COLUMN e2e_epoch    bigint,
    ADD COLUMN e2e_revision integer,
    ADD COLUMN e2e_service  boolean NOT NULL DEFAULT false;

ALTER TABLE channel_messages ADD CONSTRAINT channel_messages_e2e_header_whole CHECK (
    (e2e_envelope IS NULL AND e2e_epoch IS NULL AND e2e_revision IS NULL AND NOT e2e_service)
    OR (e2e_envelope IS NOT NULL AND e2e_epoch IS NOT NULL AND e2e_revision IS NOT NULL
        AND e2e_epoch >= 1 AND e2e_revision >= 0)
);

-- Beside an envelope there is nothing a person wrote, nor anything that
-- names it: the same list as messages_e2e_is_sealed (026), in the columns a
-- room row has (the reply and forward previews, the quote, the thread a
-- reply would put it in, the mirror of a broadcast post).
ALTER TABLE channel_messages ADD CONSTRAINT channel_messages_e2e_is_sealed CHECK (
    e2e_envelope IS NULL OR (
        content IS NULL AND content_ct IS NULL
        AND entities_ct IS NULL AND extras_ct IS NULL AND link_preview_ct IS NULL
        AND file_size IS NULL
        AND voice_duration_ms IS NULL AND voice_waveform IS NULL
        AND reply_to_id IS NULL AND reply_to_username IS NULL
        AND reply_to_author_id IS NULL AND reply_to_attachment_s3_key IS NULL
        AND forwarded_from_username IS NULL AND forwarded_from_author_id IS NULL
        AND quoted_post_id IS NULL AND quoted_channel_id IS NULL AND quoted_channel_name IS NULL
        AND quoted_channel_avatar IS NULL AND quoted_channel_avatar_thumb IS NULL
        AND quoted_post_author IS NULL AND quoted_post_attachment_s3_key IS NULL
        AND group_id IS NULL AND thread_root_id IS NULL AND mirror_of IS NULL
        AND mentioned_user_ids IS NULL AND mentioned_role_ids IS NULL
        AND NOT mentions_everyone AND NOT has_link
        -- The pointer comes whole: a kind without a file is not a file.
        AND (attachment_type IS NULL OR attachment_s3_key IS NOT NULL)
    )
);

-- Reactions on a closed room's message: each person's whole set, one
-- envelope, replaced by a higher revision (027). Not erased with the account,
-- like e2e_reactions: what a person did that others read.
CREATE TABLE e2e_channel_reactions (
    message_id uuid NOT NULL REFERENCES channel_messages(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL,
    envelope   bytea NOT NULL,
    epoch      bigint NOT NULL CHECK (epoch >= 1),
    revision   integer NOT NULL CHECK (revision >= 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (message_id, user_id)
);
