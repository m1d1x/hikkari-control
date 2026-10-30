-- 002 — the history watermark a participant draws for themselves.
--
-- `hidden_at` (baseline) already answers "is this row in my list", and
-- dialogs.go has read it since before the squash. What nothing recorded was
-- "how far back may I still SEE" — so «очистить историю» had no column to
-- write and the operation did not exist on the wire at all.
--
-- The two are deliberately separate facts, not one flag:
--   * hidden_at  — the ROW is gone from my list, until a new message brings it
--                  back. Telegram's «удалить чат» for one side.
--   * cleared_at — everything created at or before this instant does not exist
--                  for me, in the list preview, in backward history, and in the
--                  core's local store. It is never lifted by a new message.
--
-- «Удалить чат у себя» writes both; «очистить историю» writes only cleared_at,
-- which is exactly the difference between the chat leaving the list and the
-- chat staying there empty.
ALTER TABLE conversation_participants
    ADD COLUMN IF NOT EXISTS cleared_at timestamptz;
