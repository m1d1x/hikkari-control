-- 016 — a deleted account is a tombstone, not a missing row.
--
-- The owner's model (02.10, as in Telegram): when someone deletes their
-- account, everything they WROTE stays where they wrote it — the other side of
-- a DM keeps the whole conversation, text included — and the author reads as
-- «удалённый аккаунт». Everything ABOUT them goes: name, email, password,
-- avatar, birthday, status, sessions, friends, settings, server membership.
-- auth.DeleteUserAndData does that scrub; this migration gives it the marker
-- and makes the schema say the same thing.
--
-- Before this, the schema said three contradictory things at once:
--   * messages/channel_messages.sender_id were NOT NULL with ON DELETE SET
--     NULL — so `DELETE FROM users` failed for anyone who had ever written;
--   * relationships (both sides) and fraud_scoring_log had no ON DELETE at
--     all — so it also failed for anyone with a single friend;
--   * and account deletion (self, admin, the purger) was exactly that DELETE.
-- No account that had been used could be deleted.

ALTER TABLE users ADD COLUMN deleted_at timestamptz;

-- An author is never deleted, only tombstoned: a hard DELETE of someone with
-- messages is a bug, and RESTRICT makes it say so instead of nulling a NOT
-- NULL column halfway through a cascade.
ALTER TABLE messages DROP CONSTRAINT messages_sender_id_fkey;
ALTER TABLE messages ADD CONSTRAINT messages_sender_id_fkey
    FOREIGN KEY (sender_id) REFERENCES users(id) ON DELETE RESTRICT;
ALTER TABLE channel_messages DROP CONSTRAINT channel_messages_sender_id_fkey;
ALTER TABLE channel_messages ADD CONSTRAINT channel_messages_sender_id_fkey
    FOREIGN KEY (sender_id) REFERENCES users(id) ON DELETE RESTRICT;

-- Rows that are only about the person go with the person. The tombstone scrub
-- deletes them explicitly; CASCADE keeps a hard delete of a never-used account
-- (tests, fixtures) from tripping over them.
ALTER TABLE relationships DROP CONSTRAINT relationships_user_id_fkey;
ALTER TABLE relationships ADD CONSTRAINT relationships_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE relationships DROP CONSTRAINT relationships_target_id_fkey;
ALTER TABLE relationships ADD CONSTRAINT relationships_target_id_fkey
    FOREIGN KEY (target_id) REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE fraud_scoring_log DROP CONSTRAINT fraud_scoring_log_user_id_fkey;
ALTER TABLE fraud_scoring_log ADD CONSTRAINT fraud_scoring_log_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;
