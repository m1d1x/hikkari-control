-- 026 — an E2E direct-chat message may point at a file (roadmap 76.2.2.4,
-- plan 76 §1.28–1.29).
--
-- The server hands a file only to someone in a chat whose message points at
-- it (files.CanRead joins messages on attachment_s3_key / file_id). An
-- envelope hides everything, the pointer included, and the peer could not
-- download what they were sent. So an envelope row keeps, in the clear, the
-- pointer — the random storage key and the file row — and the KIND of
-- attachment (image | video | audio | file | voice), which the recipient's
-- «who may send me voice notes» setting is checked against. Everything a
-- person chose about the file stays inside the envelope: its name, size,
-- dimensions, duration, waveform and blur preview. The bytes themselves are
-- 76.3.
--
-- The owner's measure (plan §1.27): content is always sealed; a fact like
-- «X sends Y voice notes» stays readable when hiding it breaks a feature.
--
-- Replaces 025's messages_e2e_is_sealed; the rest of it is unchanged.

ALTER TABLE messages DROP CONSTRAINT messages_e2e_is_sealed;

ALTER TABLE messages ADD CONSTRAINT messages_e2e_is_sealed CHECK (
    e2e_envelope IS NULL OR (
        content IS NULL AND content_ct IS NULL
        AND entities_ct IS NULL AND extras_ct IS NULL AND link_preview_ct IS NULL
        AND file_size IS NULL AND attachment_meta IS NULL
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
        -- The pointer comes whole: a kind without a file is not a file.
        AND (attachment_type IS NULL OR attachment_s3_key IS NOT NULL)
    )
);
