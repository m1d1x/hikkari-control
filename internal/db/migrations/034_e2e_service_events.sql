-- 034 — sealed service events of an E2E direct chat (pivot 76.2′ Р5).
--
-- In an E2E DM a pin is a message of its own: an envelope in the chat log
-- that says «pinned / unpinned message X», which the server cannot read. The
-- sender marks such a message as a SERVICE event, and this is the mark. It
-- tells the server only that the message is one — not what it says, nor which
-- message it is about — so GetPinnedMessages can hand a device every service
-- event of the chat, and a device that has not loaded the whole history folds
-- them into the pin bar.
--
-- A mark on a plaintext row would be a plaintext pin path the server does not
-- have: only an envelope may carry it.
--
-- (032 is taken twice by unmerged branches — archived chats, e2e security
-- alerts — and one of them will move to 033; this one skips both.)

ALTER TABLE messages ADD COLUMN e2e_service boolean NOT NULL DEFAULT false;

ALTER TABLE messages ADD CONSTRAINT messages_e2e_service_is_sealed
    CHECK (NOT e2e_service OR e2e_envelope IS NOT NULL);

CREATE INDEX messages_e2e_service_by_conversation
    ON messages (conversation_id, created_at)
    WHERE e2e_service;
