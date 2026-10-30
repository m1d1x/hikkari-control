-- 013 — every avatar is a pair: the original and its 120-square thumb.
--
-- The wire now carries one `Avatar { full, thumb }` wherever a picture of a
-- person or a place travels, and every circle in the client draws `thumb`.
-- Users and rooms already kept both keys; two places kept only the original:
--
--   servers           — a server's picture had no thumb column at all, so the
--                       rail and the chat list drew the full image in a circle;
--   *messages.quoted_ — a quoted post denormalises its channel's picture onto
--   channel_avatar      the message, and only the original was copied.
--
-- Rows written before this carry no thumb, and are left as they are: an Avatar
-- whose thumb is absent draws the initial, the same as no picture, until the
-- owner sets the picture again.
ALTER TABLE servers
    ADD COLUMN IF NOT EXISTS avatar_thumb_s3_key text;

-- The read rule (internal/files/canread.go, case 5) looks a key up by this
-- column, same as the original's.
CREATE INDEX IF NOT EXISTS idx_servers_avatar_thumb_s3_key
    ON servers USING btree (avatar_thumb_s3_key)
    WHERE (avatar_thumb_s3_key IS NOT NULL);

ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS quoted_channel_avatar_thumb text;

ALTER TABLE channel_messages
    ADD COLUMN IF NOT EXISTS quoted_channel_avatar_thumb text;
