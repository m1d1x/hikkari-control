-- The stand measures the fleet, not one account's batch (internal/stand).
--
-- Until now stand_settings.subscription named an account, and the stand
-- measured the profiles of its subscription: the two relays that account drew.
-- A relay no account drew was measured by nobody, and its knobs went to its
-- first subscriber as they were set at its birth (06.11: amsterdam-1-001 kept
-- fp=chrome on an address that cuts Chrome 133). Now a claimed run fetches
-- every relay a subscriber can be handed, with the relay's own credential, from
-- a link of its own; the account is no longer part of the stand.
ALTER TABLE stand_settings ADD COLUMN IF NOT EXISTS enabled boolean NOT NULL DEFAULT false;
UPDATE stand_settings SET enabled = (subscription <> '');
ALTER TABLE stand_settings DROP COLUMN subscription;

-- A claimed run's capability to read the fleet's links: the SHA-256 of a
-- random token handed to the claiming point only, cleared when the run ends.
ALTER TABLE stand_tasks ADD COLUMN IF NOT EXISTS links_token_hash bytea;
CREATE UNIQUE INDEX IF NOT EXISTS stand_tasks_links_token ON stand_tasks (links_token_hash) WHERE links_token_hash IS NOT NULL;
