-- 42 П9.6б: the two repairs that bring a box's own tunnel back without
-- changing its address.
--
-- `restart` restarts the relay units the grid found stopped; `redest` moves a
-- relay off a cover site that died onto the ones that are still answering.
-- Both are jobs rather than bare ssh calls for the same reason `rekey` is: one
-- at a time per box, steps and a live log in the console, and a record of what
-- the hub did to a machine that somebody will later ask about.
ALTER TABLE entry_jobs
    DROP CONSTRAINT entry_jobs_kind_check,
    DROP CONSTRAINT entry_jobs_rollout_kind,
    ADD CONSTRAINT entry_jobs_kind_check
        CHECK (kind IN ('provision', 'canary', 'update', 'retire', 'rekey', 'reboot',
                        'restart', 'redest')),
    ADD CONSTRAINT entry_jobs_rollout_kind
        CHECK ((kind IN ('provision', 'retire', 'rekey', 'reboot', 'restart', 'redest'))
               = (rollout_id IS NULL));
