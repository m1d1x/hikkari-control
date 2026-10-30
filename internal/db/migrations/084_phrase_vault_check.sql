-- 084 — «помните пин?» answered by the server, on a counter of its own
-- (plan 76 §1.40, 76.10).
--
-- A device that never typed the PIN (one linked by QR) has nothing to check
-- the reminder's answer against, and the sign-in check spends one of the ten
-- tries that, run out, wipe the share. check_pin is the third way: it answers
-- «is this the PIN» for a signed-in device and counts wrong answers apart —
-- nine an hour, then a pause — so being reminded never wipes the phrase.
--
--   * check_failures — wrong answers in the current window;
--   * checks_since   — when the window opened (the first wrong answer); a
--                      right answer closes it.
--
-- Its own migration and not a change to 080: 080 is already in main, and the
-- runner keys by file name, so an edit to it would never reach a database
-- that ran it.

ALTER TABLE e2e_phrase_vaults
    ADD COLUMN check_failures integer NOT NULL DEFAULT 0 CHECK (check_failures >= 0),
    ADD COLUMN checks_since timestamptz;
