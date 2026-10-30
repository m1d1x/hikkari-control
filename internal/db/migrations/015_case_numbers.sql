-- 015 — an обращение is named by a random number, not by its row id.
--
-- The number is what the user reads («обращение №482913 принято») and what
-- they quote back to the team. A row id leaks the volume — «№1», «№7» says
-- exactly how many people have ever written — and invites guessing the next
-- one. `id` stays the internal key every FK and admin route uses; `case_no` is
-- only ever shown.
--
-- Six digits: short enough to read aloud, 900 000 values. The default draws
-- until it misses an existing number, so a collision costs one more draw, not a
-- failed insert; two concurrent inserts drawing the same unused number is the
-- one case left to the unique index.

CREATE FUNCTION bug_ticket_case_no() RETURNS bigint
LANGUAGE plpgsql VOLATILE AS $$
DECLARE
    n bigint;
BEGIN
    LOOP
        n := 100000 + floor(random() * 900000)::bigint;
        EXIT WHEN NOT EXISTS (SELECT 1 FROM bug_tickets WHERE case_no = n);
    END LOOP;
    RETURN n;
END
$$;

ALTER TABLE bug_tickets ADD COLUMN case_no bigint;
UPDATE bug_tickets SET case_no = bug_ticket_case_no() WHERE case_no IS NULL;
ALTER TABLE bug_tickets
    ALTER COLUMN case_no SET DEFAULT bug_ticket_case_no(),
    ALTER COLUMN case_no SET NOT NULL;
CREATE UNIQUE INDEX uq_bug_tickets_case_no ON bug_tickets (case_no);
