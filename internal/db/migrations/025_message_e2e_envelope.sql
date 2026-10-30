-- 025 — an E2E direct-chat message is an envelope (roadmap 76.2.2.3, plan 76 §9).
--
-- The envelope is AES-GCM under the chat history key of one epoch, signed by
-- the sending device (nekko-e2e envelope.rs). The server stores it whole and
-- reads only its clear header — the epoch it was sealed in and its revision
-- (the edit counter, plan §1.20) — into the two columns beside it, so it can
-- refuse a send sealed in a stale epoch and an edit that does not move the
-- revision forward. Who sent it from which device is inside; the row's
-- sender_id is the account the socket belongs to, as for every message.
--
-- The CHECK is the database's half of «an E2E row carries no plaintext»: a
-- code path that wrote content, entities, a reply preview, a link card, a
-- mention or an attachment beside an envelope fails here even if the Go
-- check in front of it were removed. Media in E2E chats is 76.3; until then an
-- envelope row carries none.
--
-- New nullable columns: no existing row is touched, every existing row is a
-- plaintext-era row and satisfies the CHECK by having no envelope.

ALTER TABLE messages
    ADD COLUMN e2e_envelope bytea,
    ADD COLUMN e2e_epoch    bigint,
    ADD COLUMN e2e_revision integer;

ALTER TABLE messages ADD CONSTRAINT messages_e2e_header_whole CHECK (
    (e2e_envelope IS NULL AND e2e_epoch IS NULL AND e2e_revision IS NULL)
    -- IS NOT NULL spelled out: a NULL comparison would make the CHECK pass.
    OR (e2e_envelope IS NOT NULL AND e2e_epoch IS NOT NULL AND e2e_revision IS NOT NULL
        AND e2e_epoch >= 1 AND e2e_revision >= 0)
);

ALTER TABLE messages ADD CONSTRAINT messages_e2e_is_sealed CHECK (
    e2e_envelope IS NULL OR (
        content IS NULL AND content_ct IS NULL
        AND entities_ct IS NULL AND extras_ct IS NULL AND link_preview_ct IS NULL
        AND attachment_s3_key IS NULL AND attachment_type IS NULL AND file_size IS NULL
        AND file_id IS NULL AND attachment_meta IS NULL
        AND voice_duration_ms IS NULL AND voice_waveform IS NULL
        AND reply_to_id IS NULL AND reply_to_username IS NULL AND reply_to_author_id IS NULL
        AND reply_to_attachment_s3_key IS NULL
        AND forwarded_from_username IS NULL AND forwarded_from_author_id IS NULL
        AND quoted_post_id IS NULL AND quoted_channel_id IS NULL AND quoted_channel_name IS NULL
        AND quoted_channel_avatar IS NULL AND quoted_channel_avatar_thumb IS NULL
        AND quoted_post_author IS NULL AND quoted_post_attachment_s3_key IS NULL
        AND group_id IS NULL
        AND mentioned_user_ids IS NULL AND mentioned_role_ids IS NULL
        AND NOT mentions_everyone AND NOT has_link
        AND message_type = ''
    )
);
