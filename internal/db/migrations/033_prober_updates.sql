-- The hub owns prober builds and keeps failed/silent slots visible.
ALTER TABLE entry_vantages
    ADD COLUMN IF NOT EXISTS prober_arch text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS prober_sha text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS prober_poll_at timestamptz,
    ADD COLUMN IF NOT EXISTS prober_update_error text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS prober_failed_sha text NOT NULL DEFAULT '';

-- Preserve rollback identity across a hub restart during probation. These are
-- token hashes, never bearer tokens or SSH passwords.
CREATE TABLE entry_prober_deployments (
    box text PRIMARY KEY REFERENCES entry_boxes(name) ON DELETE CASCADE,
    slot text NOT NULL REFERENCES entry_vantages(slot) ON DELETE CASCADE,
    prior_token_hash bytea,
    deployed_token_hash bytea NOT NULL
);
