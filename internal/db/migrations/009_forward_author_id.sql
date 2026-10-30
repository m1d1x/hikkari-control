-- 009 — a forward remembers WHO wrote the original, not just their name.
--
-- The forward snapshot had `forwarded_from_username` and nothing that
-- identifies the author, which is the same gap migration 008 closed for
-- replies: a display name cannot answer «is this mine?», and that question
-- decides the colour of the «Переслано от …» header and whether it reads «Вы».
--
-- The gap was invisible for a different reason than 008's, though. A forward
-- carried no attribution at all until now: the client built the origin, put it
-- in its local payload block, and the bridge's send path had no field to take
-- it — so the forwarder saw the header on their own optimistic row and nobody
-- else ever did. Now that the origin crosses (ForwardRef.author_id), the row
-- needs somewhere to keep it.
--
-- Nullable with no backfill, for the same reason as 008: no row written before
-- this can have the id recovered, and guessing it from a message that may since
-- have been edited or deleted is exactly what a denormalised snapshot exists to
-- avoid.
ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS forwarded_from_author_id text;

ALTER TABLE channel_messages
    ADD COLUMN IF NOT EXISTS forwarded_from_author_id text;
