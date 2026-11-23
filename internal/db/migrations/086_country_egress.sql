CREATE TABLE entry_country_egress (
    entry_box text PRIMARY KEY REFERENCES trunk_entries(entry_box),
    de_box text NOT NULL REFERENCES entry_boxes(name),
    nl_port integer NOT NULL CHECK (nl_port BETWEEN 1024 AND 65535),
    de_port integer NOT NULL CHECK (de_port BETWEEN 1024 AND 65535),
    auto_port integer NOT NULL CHECK (auto_port BETWEEN 1024 AND 65535),
    sni text NOT NULL DEFAULT '',
    border_sha text NOT NULL DEFAULT '',
    staged_sha text NOT NULL DEFAULT '',
    applied_sha text NOT NULL DEFAULT '',
    CHECK (entry_box <> de_box),
    CHECK (nl_port <> de_port AND nl_port <> auto_port AND de_port <> auto_port)
);

ALTER TABLE entry_cascades ADD COLUMN country_relay bigint REFERENCES entry_relays(id) ON DELETE SET NULL;

ALTER TABLE entry_jobs DROP CONSTRAINT entry_jobs_kind_check;
ALTER TABLE entry_jobs ADD CONSTRAINT entry_jobs_kind_check CHECK (kind IN (
    'provision', 'canary', 'update', 'retire', 'rekey', 'reboot', 'restart', 'redest', 'prober',
    'reconfigure', 'upgrade', 'country_egress'));
ALTER TABLE entry_jobs DROP CONSTRAINT entry_jobs_rollout_kind;
ALTER TABLE entry_jobs ADD CONSTRAINT entry_jobs_rollout_kind CHECK ((kind NOT IN ('canary', 'update')) = (rollout_id IS NULL));
