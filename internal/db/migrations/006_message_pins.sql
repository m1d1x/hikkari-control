-- 006 — a pinned message is chat state, not a decoration on the client.
--
-- Until now «закрепить» moved a list inside one running client: nobody else saw
-- it and it died with the process. This table is where it becomes a fact of the
-- chat — the same promotion reactions got, and it is built the same way.
--
-- Keyed by message_id ALONE, like message_reactions, and deliberately not a
-- column on the per-type message tables. A pin has to mean the same thing for a
-- DM row in `messages` and a room row in `channel_messages`, and a mirrored post
-- shares one id across both of its surfaces — a shared table keeps that one
-- fact in one place, while two columns would be two facts drifting apart.
--
-- chat_type/chat_id are carried anyway, redundantly with the message row, for
-- the one question the message tables cannot answer cheaply: «what is pinned in
-- THIS chat» — the pin bar's query, which must not fan out over every message
-- table to find out.
--
-- pinned_at is the order the bar reads (newest pin last), which is why it is a
-- timestamp and not a bool anywhere on the wire: an order derived from message
-- time would reshuffle the bar the moment somebody pinned an old message.
--
-- pinned_by is kept for audit and for the «X закрепил сообщение» plate that a
-- room will eventually want. Nothing reads it today; it costs one column now and
-- an unrecoverable gap in history if it is added later.
CREATE TABLE IF NOT EXISTS message_pins (
    message_id uuid PRIMARY KEY,
    chat_type  smallint NOT NULL,
    chat_id    uuid NOT NULL,
    pinned_by  uuid NOT NULL,
    pinned_at  timestamp with time zone NOT NULL DEFAULT now()
);

-- The pin bar's read: one chat's pins in pin order.
CREATE INDEX IF NOT EXISTS message_pins_chat_idx
    ON message_pins (chat_type, chat_id, pinned_at);
