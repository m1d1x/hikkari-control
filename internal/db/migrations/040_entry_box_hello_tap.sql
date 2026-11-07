-- The hello tap of a Reality edge (edge/hellotap.go; hikkari-client
-- plans/2026-11-07-knob-truth-wire-diagnostics.md, W5): on, the hub writes
-- EDGE_HELLO_TAP=1 into the env of the box's relays at their next install,
-- and the edge counts the ClientHellos that reach it. Off for every box until
-- the owner turns it on for one (`hub entry-box hello-tap <box> on`).
ALTER TABLE entry_boxes ADD COLUMN IF NOT EXISTS hello_tap boolean NOT NULL DEFAULT false;

-- The setting is applied by a job of its own, 'reconfigure': the box moved
-- onto the image's build with the env its settings give, outside any rollout
-- (Orchestrator.UpdateBox), so it carries no rollout_id.
ALTER TABLE entry_jobs DROP CONSTRAINT IF EXISTS entry_jobs_kind_check;
ALTER TABLE entry_jobs ADD CONSTRAINT entry_jobs_kind_check CHECK (kind = ANY (ARRAY[
    'provision', 'canary', 'update', 'retire', 'rekey', 'reboot', 'restart', 'redest', 'prober', 'reconfigure']));
ALTER TABLE entry_jobs DROP CONSTRAINT IF EXISTS entry_jobs_rollout_kind;
ALTER TABLE entry_jobs ADD CONSTRAINT entry_jobs_rollout_kind CHECK ((kind = ANY (ARRAY[
    'provision', 'retire', 'rekey', 'reboot', 'restart', 'redest', 'prober', 'reconfigure'])) = (rollout_id IS NULL));
