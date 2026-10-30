-- 008 — a reply remembers WHO it quotes, not just what their name was.
--
-- The denormalised reply snapshot carried `reply_to_username` and nothing that
-- identifies the author. A display name cannot answer the one question the
-- reference's own rendering asks — «is this my message?» — which decides the
-- accent colour and whether the label reads «Вы» instead of a name.
--
-- The absence was visible as a flicker. A sending client knew the source's
-- author locally, so it drew the reference as its own; the echo came back
-- through a wire form that had no room for the id, the snapshot was rebuilt
-- without it, and the quote changed colour mid-send. On the RECEIVING side the
-- same gap was quieter and worse: a reply to their own message never read as
-- theirs, because nothing on the row ever said so.
--
-- Nullable with no backfill, deliberately. Rows written before this column
-- cannot have the id recovered — resolving it from the source message today
-- would be a guess about a message that may since have been edited or deleted,
-- and the whole point of a denormalised snapshot is that it is not re-derived.
-- An old reply keeps rendering exactly as it does now (an author that is not
-- "you"); new ones are right.
ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS reply_to_author_id text;

ALTER TABLE channel_messages
    ADD COLUMN IF NOT EXISTS reply_to_author_id text;
