-- 003 — a quote of a FRAGMENT stops reading as a reply to the whole message.
--
-- The reply snapshot the sender denormalises (`reply_to_username`,
-- `reply_to_content`, …) had room for the preview TEXT and nothing else, so the
-- two facts that make a quote a quote had nowhere to land: the preview's own
-- formatting, and which run of the source was quoted. Both survived only on the
-- sending device, in its local payload block — and vanished from the sender's
-- own row the moment the server echo promoted it.
--
--   * reply_to_entities — formatting over `reply_to_content`, same JSON shape
--     the entity codec already writes (internal/messaging/entities.go). ONE
--     system of coordinates: `reply_to_content` is the preview — the whole text
--     for a plain reply, only the quoted run for a quote — and the entities are
--     always relative to it.
--   * reply_to_quote_start / _end — where that run sat in the SOURCE, in UTF-16
--     code units, start inclusive and end exclusive. Both 0 means "not a quote,
--     a whole-message reply", which is also what every row written before this
--     migration says. That is what makes the default honest rather than a
--     guess: no backfill can recover a span nobody recorded.
--
-- Plaintext, beside `reply_to_content` rather than sealed like `entities_ct`:
-- the text these describe is already in the clear on this row, so sealing the
-- formatting alone would protect nothing while doubling the columns. The whole
-- reply block converts together when the plaintext-column track (audit В10)
-- reaches it.
ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS reply_to_entities text,
    ADD COLUMN IF NOT EXISTS reply_to_quote_start integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS reply_to_quote_end integer NOT NULL DEFAULT 0;

ALTER TABLE channel_messages
    ADD COLUMN IF NOT EXISTS reply_to_entities text,
    ADD COLUMN IF NOT EXISTS reply_to_quote_start integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS reply_to_quote_end integer NOT NULL DEFAULT 0;
