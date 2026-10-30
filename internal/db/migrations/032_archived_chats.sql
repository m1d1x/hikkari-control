-- 032 — the chat archive: chats the account moved out of its main list.
--
-- A sibling of pinned_chats and manual_unreads: one row per (account, chat),
-- account-scoped and synced through the settings events. A new table, so no
-- existing row is touched.
--
-- Only the two addressable spellings are admitted. The older tables still
-- carry the pre-task-21 'room'/'server' spellings in their CHECKs; this one is
-- born after that collapse and has no reason to accept them.
--
-- The index on (chat_type, chat_id) serves the server-side unarchive: a message
-- landing in a chat asks «who archived THIS chat», not «what did this account
-- archive», and it asks on every send.

CREATE TABLE archived_chats (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    chat_type text NOT NULL CHECK (chat_type IN ('dm', 'channel')),
    chat_id uuid NOT NULL,
    archived_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, chat_type, chat_id)
);

CREATE INDEX idx_archived_chats_chat ON archived_chats (chat_type, chat_id);
