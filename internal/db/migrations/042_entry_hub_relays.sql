-- 042 — relays are born on the hub (roadmap 42 П6.15, decision 2026-10-19).
--
-- Until now a relay was minted, deployed and signed on the owner's machine and
-- arrived here as a signed record (036). The signing key and the registry of
-- relay identities moved to the hub: it mints a relay, deploys it, probes it
-- and only then signs it into the pool — so a relay exists here from birth,
-- before it has a record.
--
--   pending — born (identity minted, maybe deployed), not admitted: no record,
--             never handed out. The admission probe has not passed yet.
--   alive   — admitted and signed: the record is handed out.
--   dead    — retired, for good; a replacement gets a new name.
--
-- identity_ct is the relay's whole identity (x25519 keys, short id, VLESS id,
-- covers, port) sealed under the server key: the secrets nekko-edge runs with.
-- The record (envelope_ct) is derived from it plus the box's address at signing
-- time and re-derived at every re-sign.
--
-- A relay now names its box by foreign key: the hub created both, so the link
-- is a fact, not an address match (039 grouped by address because records came
-- from outside).
--
-- The pool so far held test relays signed on the owner's machine, with no
-- identity the hub could deploy or re-sign; it is emptied (owner's decision —
-- no users, test data only).

TRUNCATE entry_fail_reports, entry_grants, entry_relays, entry_boxes RESTART IDENTITY;

ALTER TABLE entry_relays
    DROP CONSTRAINT entry_relays_status_check,
    DROP CONSTRAINT entry_relays_dead_has_time,
    ALTER COLUMN status SET DEFAULT 'pending',
    ALTER COLUMN key_id DROP NOT NULL,
    ALTER COLUMN envelope_ct DROP NOT NULL,
    ALTER COLUMN issued_at DROP NOT NULL,
    ALTER COLUMN expires_at DROP NOT NULL,
    ALTER COLUMN imported_at DROP NOT NULL,
    ALTER COLUMN imported_at DROP DEFAULT,
    ADD COLUMN box text NOT NULL REFERENCES entry_boxes (name) ON DELETE RESTRICT,
    ADD COLUMN identity_ct bytea NOT NULL,
    ADD COLUMN created_at timestamptz NOT NULL DEFAULT now(),
    ADD CONSTRAINT entry_relays_status_check CHECK (status IN ('pending', 'alive', 'dead')),
    ADD CONSTRAINT entry_relays_dead_has_time CHECK ((status = 'dead') = (died_at IS NOT NULL)),
    -- A record is all of it or none of it; an alive relay always has one.
    ADD CONSTRAINT entry_relays_record_whole CHECK (
        (envelope_ct IS NULL) = (key_id IS NULL)
        AND (envelope_ct IS NULL) = (issued_at IS NULL)
        AND (envelope_ct IS NULL) = (expires_at IS NULL)
        AND (envelope_ct IS NULL) = (imported_at IS NULL)),
    ADD CONSTRAINT entry_relays_alive_has_record CHECK (status <> 'alive' OR envelope_ct IS NOT NULL);

-- imported_at now means "last signed": the hub signs, nothing is imported.
ALTER TABLE entry_relays RENAME COLUMN imported_at TO signed_at;

CREATE INDEX entry_relays_by_box ON entry_relays (box);

-- Boxes are created by the hub too.
ALTER TABLE entry_boxes RENAME COLUMN imported_at TO created_at;
