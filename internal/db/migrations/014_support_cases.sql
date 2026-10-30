-- 014 — a support conversation carries a status.
--
-- Support is one channel: the user's chat with the official account «Nekko».
-- A bug report filed through the form was the only thing with a status; a user
-- who simply wrote into the chat had filed nothing, so the team could neither
-- mark it «в работе» nor close it, and the user got no acknowledgement. Now
-- the first message of a new request opens a case of type `question` in the
-- same table the bug reports live in: one status vocabulary, one admin surface,
-- one milestone writer.

ALTER TABLE bug_tickets DROP CONSTRAINT bug_tickets_ticket_type_check;
ALTER TABLE bug_tickets ADD CONSTRAINT bug_tickets_ticket_type_check
    CHECK (ticket_type = ANY (ARRAY['bug'::text, 'idea'::text, 'question'::text]));

-- At most ONE active question per user. The intake opens a case after a send
-- commits, off the request path; two messages sent back to back would both
-- find «no active case» and open two. The index makes the second insert a
-- no-op instead (ON CONFLICT DO NOTHING), so the rule holds under any timing.
CREATE UNIQUE INDEX uq_bug_tickets_active_question
    ON bug_tickets (user_id)
    WHERE ticket_type = 'question' AND status IN ('open', 'analyzed');
