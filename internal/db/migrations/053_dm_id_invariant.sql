-- 053 — a DM's id is its pair's, as a property of the table (recheck 76.2, R9).
--
-- Every client derives the id of the DM between two accounts and refuses a DM
-- under any other (internal/dmid.ChatID, client-core nekko-e2e `dm_chat_id`).
-- So a conversation row written past dmid.Create is a chat no client accepts.
-- Until now that was held by a test that grepped Go sources for an INSERT,
-- which another spelling of the same SQL walked around, and which never saw
-- .sql seeds. Now the row names its two accounts and the CHECK below re-derives
-- the id from them: no writer, in any language, can store a DM under an id
-- that is not its pair's.
--
-- `dm_chat_id` is the same derivation as dmid.ChatID — SHA-256 over
-- 'nekko-dm-chat-v1' ‖ 0 ‖ low ‖ 0 ‖ high, the accounts as canonical UUID text
-- in byte order, cut to 16 bytes, stamped as UUID version 8 — and it is held
-- to the same fixture (internal/dmid TestTheDatabaseDerivesTheSameDMIDs).
-- uuid comparison is byte order, which is the text order of canonical
-- lowercase hex, so LEAST/GREATEST pick the same low/high as Go.
--
-- Existing rows take their pair from their two participants. A row that has
-- no such pair, or whose id is not that pair's, fails the constraint and the
-- migration with it: nothing is deleted on the way (the local stand is
-- rebuilt instead).

CREATE FUNCTION dm_chat_id(a uuid, b uuid) RETURNS uuid
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT encode(
        set_byte(
            set_byte(d, 6, (get_byte(d, 6) & 15) | 128),
            8, (get_byte(d, 8) & 63) | 128
        ),
        'hex'
    )::uuid
    FROM (
        SELECT substring(sha256(
            'nekko-dm-chat-v1'::bytea || '\x00'::bytea
            || LEAST(a, b)::text::bytea || '\x00'::bytea
            || GREATEST(a, b)::text::bytea
        ) FROM 1 FOR 16) AS d
    ) AS digest
$$;

ALTER TABLE conversations
    -- As the participants' own rows: an account that is really deleted takes
    -- its DMs with it.
    ADD COLUMN user_low uuid REFERENCES users(id) ON DELETE CASCADE,
    ADD COLUMN user_high uuid REFERENCES users(id) ON DELETE CASCADE;

UPDATE conversations c
   SET user_low = p.low, user_high = p.high
  FROM (
        -- Text order is byte order for canonical lowercase UUIDs (uuid has
        -- no min/max aggregate).
        SELECT conversation_id, min(user_id::text)::uuid AS low, max(user_id::text)::uuid AS high
          FROM conversation_participants
         GROUP BY conversation_id
        HAVING count(*) = 2
       ) p
 WHERE p.conversation_id = c.id;

ALTER TABLE conversations
    ALTER COLUMN user_low SET NOT NULL,
    ALTER COLUMN user_high SET NOT NULL,
    ALTER COLUMN id DROP DEFAULT,
    ADD CONSTRAINT conversations_one_pair UNIQUE (user_low, user_high),
    ADD CONSTRAINT conversations_id_is_the_pairs CHECK (
        user_low < user_high AND id = dm_chat_id(user_low, user_high)
    );
