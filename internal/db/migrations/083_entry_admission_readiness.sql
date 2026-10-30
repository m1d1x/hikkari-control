-- A live admission worker must be observable with an empty fleet. Relay
-- verdicts describe relays and disappear when none exist; successful admission
-- task polls prove the worker can accept the first machine. Existing data and
-- tokens stay intact; the next authenticated poll fills this timestamp.
ALTER TABLE entry_vantages ADD COLUMN last_admission_poll_at timestamptz;
