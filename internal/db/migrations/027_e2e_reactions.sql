-- 027 — reactions on an E2E direct-chat message (roadmap 76.2.2.4, plan 76 §1.24).
--
-- The emoji is content; who reacted to which message is not (owner decision
-- §1.24: the target stays visible, so «delete for everyone» takes a message's
-- reactions with it, §1.20). So each person's reactions on one message are ONE
-- envelope — the whole set, sealed — replaced like an edit: a higher revision
-- in the chat's current epoch, never back. Taking every reaction off deletes
-- the row. The server knows that X reacted to message N, not with what, nor
-- with how many.
--
-- The FK cascade is §1.20: a message deleted for everyone takes its reactions.
-- Like message_reactions, these are what the person did that others read, so
-- erasing an account does not delete them (internal/auth eraseSteps note).
-- New table only: no existing data is touched.

CREATE TABLE e2e_reactions (
    message_id uuid NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL,
    envelope   bytea NOT NULL,
    epoch      bigint NOT NULL CHECK (epoch >= 1),
    revision   integer NOT NULL CHECK (revision >= 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (message_id, user_id)
);
