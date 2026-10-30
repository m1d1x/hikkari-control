-- 051 — why a relay died, and a machine handed back (roadmap 42 П6.10).
--
-- A death is the abuse dataset's label (П6.7, П6.12): "the IP was banned" and
-- "the provider went down" are opposite answers to the question the model
-- learns, and a manual death with no reason is a sample thrown away. So every
-- death says why, from the first one on:
--
--   ip_banned     — the address is blocked; Reality and the bare-TLS control
--                   are down from RU while the clean control is up;
--   fingerprint   — the address is reachable, the protocol is cut;
--   destination   — the relay is fine, what it borrows or forwards to is not
--                   (cured by rotating the cover, not the address);
--   provider_down — the machine itself is gone or broken, nothing to do with
--                   a censor;
--   rotated       — retired on purpose while healthy (planned rotation);
--   other         — none of the above; the owner knows.
--
-- A box handed back to its provider is retired: every relay on it is dead
-- with one reason, the hub wiped the relay off the machine, and the box stays
-- on record (039: a box is never deleted) next to the relays that ran on it.
-- A retired box takes no new relay, no provisioning and no rollout.
--
-- Wiping the machine is a job like provisioning, so the console follows it in
-- the same panel; it belongs to no rollout.
--
-- Dead relays so far are test data the owner killed by hand; they become
-- `other`.

ALTER TABLE entry_relays
    ADD COLUMN died_reason text CHECK (died_reason IN
        ('ip_banned', 'fingerprint', 'destination', 'provider_down', 'rotated', 'other'));

UPDATE entry_relays SET died_reason = 'other' WHERE status = 'dead';

ALTER TABLE entry_relays
    ADD CONSTRAINT entry_relays_dead_has_reason CHECK ((status = 'dead') = (died_reason IS NOT NULL));

ALTER TABLE entry_boxes
    ADD COLUMN retired_at timestamptz;

ALTER TABLE entry_jobs
    DROP CONSTRAINT entry_jobs_kind_check,
    DROP CONSTRAINT entry_jobs_rollout_kind,
    ADD CONSTRAINT entry_jobs_kind_check CHECK (kind IN ('provision', 'canary', 'update', 'retire')),
    ADD CONSTRAINT entry_jobs_rollout_kind CHECK ((kind IN ('provision', 'retire')) = (rollout_id IS NULL));
