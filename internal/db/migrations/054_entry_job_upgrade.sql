-- 'upgrade': the owner's `hub entry-box update <box>`, one box moved to the
-- image's edge build now by the rollout's own update job (drain, swap,
-- probe, watch), outside any rollout, so it carries no rollout_id
-- (Orchestrator.UpdateNow; roadmap 2.2, the first hitless rollout of unl1).
ALTER TABLE entry_jobs DROP CONSTRAINT IF EXISTS entry_jobs_kind_check;
ALTER TABLE entry_jobs ADD CONSTRAINT entry_jobs_kind_check CHECK (kind = ANY (ARRAY[
    'provision', 'canary', 'update', 'retire', 'rekey', 'reboot', 'restart', 'redest', 'prober', 'reconfigure', 'upgrade']));
ALTER TABLE entry_jobs DROP CONSTRAINT IF EXISTS entry_jobs_rollout_kind;
ALTER TABLE entry_jobs ADD CONSTRAINT entry_jobs_rollout_kind CHECK ((kind = ANY (ARRAY[
    'provision', 'retire', 'rekey', 'reboot', 'restart', 'redest', 'prober', 'reconfigure', 'upgrade'])) = (rollout_id IS NULL));
