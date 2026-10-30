-- 019 — message content stops being readable from a dump.
--
-- `cryptobox` promises «pg_dump leak → ciphertext only», and `content_ct` kept
-- that promise for the message body. Everything AROUND the body did not: the
-- canary run of roadmap task 76 (scripts/e2e-canary/run.sh, 2026-10-10) found
-- the text of a message, its reply, its edit, its caption, its link and its
-- file name readable in the clear in three places:
--
--   * scope_events.payload — the durable event log stores a full protojson copy
--     of every event, message text included, as plain jsonb;
--   * messages / channel_messages — the denormalised reply preview, forwarded
--     text, quoted post text, reply formatting and the attachment's file name;
--   * files.original_name — the uploaded file's name.
--
-- The fix is one rule rather than one more sealed column: content lives in
-- `*_ct` columns only.
--
--   * The event log seals the whole payload (payload_ct). One choke point —
--     AppendEvent / ReadTail — covers every event type, present and future.
--   * The per-message satellites become ONE sealed block (extras_ct): the reply
--     preview text and its formatting, the forwarded text, the quoted post text
--     and the file name, sealed together by the writer that already builds them
--     (extractRich) and opened by the one reader (historyRow.toMessage).
--   * files.original_name becomes original_name_ct, sealed at the repository.
--
-- Names, ids and keys beside them (reply_to_username, forwarded_from_username,
-- quoted_post_author, quoted_channel_name, attachment keys) stay in the clear:
-- they are who/where, which the server knows anyway, not what was said.
--
-- Existing rows are NOT carried over — owner's decision 2026-10-10: the only
-- data is test data, the local database is recreated. The DELETE below makes
-- that explicit for any database that still has events (precedent: 136).

DELETE FROM scope_events;
ALTER TABLE scope_events DROP COLUMN payload;
ALTER TABLE scope_events ADD COLUMN payload_ct bytea NOT NULL;

ALTER TABLE messages
    DROP COLUMN reply_to_content,
    DROP COLUMN reply_to_entities,
    DROP COLUMN forwarded_from_content,
    DROP COLUMN quoted_post_content,
    DROP COLUMN file_name,
    ADD COLUMN extras_ct bytea,
    ADD COLUMN extras_key_version smallint;

ALTER TABLE channel_messages
    DROP COLUMN reply_to_content,
    DROP COLUMN reply_to_entities,
    DROP COLUMN forwarded_from_content,
    DROP COLUMN quoted_post_content,
    DROP COLUMN file_name,
    ADD COLUMN extras_ct bytea,
    ADD COLUMN extras_key_version smallint;

ALTER TABLE files
    DROP COLUMN original_name,
    ADD COLUMN original_name_ct bytea;
