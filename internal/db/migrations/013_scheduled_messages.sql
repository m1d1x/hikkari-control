-- 013 — messages the server sends later on the sender's behalf.
--
-- «Отложить отправку» (at a time, optionally repeating) and «отправить при
-- онлайне» (on the DM peer's next visible «в сети») both used to live only in
-- the client: a timer in the app that fired while the app happened to be open,
-- and nothing at all for the online trigger. A schedule is server state.
--
-- The item keeps the WHOLE send it will become — a SendMessage, sealed like
-- message content — rather than a column per field: when it fires it goes down
-- the ordinary send path (internal/messaging/scheduled.go), so every field a
-- send carries is carried without this table having to know about it.
--
-- Exactly one trigger per row. `peer_id` is the DM peer an online-triggered
-- row waits for, resolved once at schedule time so the presence edge can find
-- its rows by index instead of by joining every pending DM.
--
-- `occurrence` counts fires; it is half of each occurrence's idempotency key
-- (`sched:<id>:<occurrence>` in client_message_id), so a fire that commits
-- nothing cannot become a second message when it is retried.
--
-- `failure` holds the refusal of the last attempt (a block, a closed DM). A
-- refused item is kept and shown, not dropped; the dispatcher skips it until
-- the sender edits, reschedules or sends it.
CREATE TABLE IF NOT EXISTS scheduled_messages (
    id          uuid PRIMARY KEY,
    sender_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    chat_type   smallint NOT NULL,
    chat_id     uuid NOT NULL,
    peer_id     uuid REFERENCES users(id) ON DELETE CASCADE,
    frame_ct    bytea NOT NULL,
    send_at     timestamptz,
    when_online boolean NOT NULL DEFAULT false,
    repeat      smallint NOT NULL DEFAULT 0,
    occurrence  integer NOT NULL DEFAULT 0,
    failure     text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT scheduled_messages_one_trigger
        CHECK ((send_at IS NOT NULL) <> when_online),
    CONSTRAINT scheduled_messages_online_has_peer
        CHECK (NOT when_online OR peer_id IS NOT NULL),
    CONSTRAINT scheduled_messages_repeat_is_timed
        CHECK (repeat = 0 OR send_at IS NOT NULL)
);

-- The dispatcher's question: what is due.
CREATE INDEX IF NOT EXISTS idx_scheduled_messages_due
    ON scheduled_messages (send_at)
    WHERE send_at IS NOT NULL AND failure = '';

-- The presence edge's question: who was waiting for this account.
CREATE INDEX IF NOT EXISTS idx_scheduled_messages_peer
    ON scheduled_messages (peer_id)
    WHERE when_online AND failure = '';

-- The list a chat shows.
CREATE INDEX IF NOT EXISTS idx_scheduled_messages_chat
    ON scheduled_messages (sender_id, chat_type, chat_id);
