-- 064 — why a machine was handed back (plan 42 К6).
--
-- A relay says why it died since 051; a box only said when it was retired.
-- That gap makes the provider comparison meaningless: "19 machines bought, 2
-- retired" answers nothing until the two are split into "the subnet was
-- banned" and "handed back healthy". A winrate computed over both is a number
-- about nothing.
--
-- The vocabulary is the relay's, because retiring a box IS killing its relays
-- with one reason (Service.Retire already takes it and validates it against
-- the same list) — a second vocabulary would only let the two drift:
--
--   ip_banned     — the address is blocked;
--   fingerprint   — the address is reachable, the protocol is cut;
--   destination   — what the relay borrows or forwards to is blocked;
--   provider_down — the machine itself is gone or broken, no censor involved;
--   rotated       — handed back healthy (planned rotation);
--   other         — none of the above; the owner knows.
--
-- Boxes already retired get their reason derived from the relays that died on
-- them, once, here: the censor reasons win over the operational ones, because
-- a box whose relay was ip-banned was handed back over that ban even if a
-- later relay was merely rotated. A retired box with no dead relay on record
-- becomes 'other' — the honest answer, not a guess at a cause.

ALTER TABLE entry_boxes
    ADD COLUMN retired_reason text CHECK (retired_reason IN
        ('ip_banned', 'fingerprint', 'destination', 'provider_down', 'rotated', 'other'));

UPDATE entry_boxes b
SET retired_reason = COALESCE((
    SELECT r.died_reason
    FROM entry_relays r
    WHERE r.box = b.name AND r.died_reason IS NOT NULL
    ORDER BY CASE r.died_reason
        WHEN 'ip_banned'     THEN 1
        WHEN 'fingerprint'   THEN 2
        WHEN 'destination'   THEN 3
        WHEN 'provider_down' THEN 4
        WHEN 'rotated'       THEN 5
        ELSE 6
    END
    LIMIT 1
), 'other')
WHERE b.retired_at IS NOT NULL;

-- Fail closed from here on: a retire path that forgets the reason is a bug
-- the comparison would otherwise swallow as a machine that died of nothing.
ALTER TABLE entry_boxes
    ADD CONSTRAINT entry_boxes_retired_has_reason
        CHECK ((retired_at IS NOT NULL) = (retired_reason IS NOT NULL));

-- The comparison reads the fleet by provider and by cause, over a window.
CREATE INDEX entry_boxes_provider_life ON entry_boxes (provider, created_at, retired_at);
