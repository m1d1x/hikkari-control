-- 029 — scheduled messages in an E2E direct chat (roadmap 76.2.2.4,
-- plan 76 §1.26, §1.30).
--
-- An envelope is sealed for ONE message id, and every occurrence of a
-- repeating item is a message of its own. So an E2E item does not hold one
-- frame the server copies: it holds a STOCK of occurrences the author's
-- devices sealed ahead — each its own message id and envelope — and fires one
-- per occurrence. Any device of the author tops the stock up, re-seals what
-- has not fired yet when the chat's epoch moves (§1.26, best effort: an
-- occurrence nobody re-sealed fires in the epoch it was sealed in), or
-- replaces it with an edited text (a higher revision). An occurrence with no
-- envelope does not fire: the item waits for the author's next device to
-- come online, and resumes.
--
-- What the server learns: when the author sends to whom, which it already
-- knew from the plaintext schedule. The content is in the envelopes.
--
-- New column and table: existing items are plaintext items.

ALTER TABLE scheduled_messages ADD COLUMN e2e boolean NOT NULL DEFAULT false;

CREATE TABLE scheduled_e2e_stock (
    item_id    uuid NOT NULL REFERENCES scheduled_messages(id) ON DELETE CASCADE,
    occurrence integer NOT NULL CHECK (occurrence >= 0),
    -- The id the occurrence becomes when it fires; the envelope is bound to it.
    message_id uuid NOT NULL UNIQUE,
    envelope   bytea NOT NULL,
    epoch      bigint NOT NULL CHECK (epoch >= 1),
    revision   integer NOT NULL CHECK (revision >= 0),
    PRIMARY KEY (item_id, occurrence)
);
