-- 043 — the hub provisions relay boxes itself (roadmap 42 П6.15 Ф3).
--
-- The operator hands the hub a rented machine (address, user, the provider's
-- root password) and the hub does the rest over ssh: its own key in, password
-- login off, firewall, relays deployed from the binaries in its image, an
-- admission probe from the RU vantage, and only then the relays signed into the
-- pool. Every run is a job the console watches step by step.

-- The hub's own ssh key: one for every box it provisions. Minted on first use,
-- the private half sealed under the server key. A singleton.
CREATE TABLE entry_hub_ssh (
    id          boolean PRIMARY KEY DEFAULT true CHECK (id),
    private_ct  bytea NOT NULL,
    -- authorized_keys form ("ssh-ed25519 AAAA… nekko-hub"): what the operator
    -- pastes into a provider's panel to skip the password path.
    public_key  text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- Who the hub logs in as, and the host key it saw first (trust on first use):
-- a different key later is either a reinstalled machine or someone in the
-- middle, and the hub stops rather than guess.
ALTER TABLE entry_boxes
    ADD COLUMN ssh_user text NOT NULL DEFAULT 'root' CHECK (ssh_user ~ '^[a-z_][a-z0-9_-]{0,31}$'),
    ADD COLUMN host_key text;

CREATE TABLE entry_jobs (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    box         text NOT NULL REFERENCES entry_boxes (name) ON DELETE RESTRICT,
    kind        text NOT NULL CHECK (kind IN ('provision')),
    status      text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'done', 'failed')),
    -- The step running now, or the one that failed: what the console shows.
    step        text NOT NULL DEFAULT '',
    error       text,
    -- The operator who started it; no FK — the admin plane is its own (077).
    admin_id    uuid,
    created_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    CONSTRAINT entry_jobs_finished CHECK ((status = 'running') = (finished_at IS NULL)),
    CONSTRAINT entry_jobs_failed_says_why CHECK ((status = 'failed') = (error IS NOT NULL))
);

-- One job per box at a time: two runs on one machine would race each other's
-- systemctl and firewall.
CREATE UNIQUE INDEX entry_jobs_one_running_per_box ON entry_jobs (box) WHERE status = 'running';
CREATE INDEX entry_jobs_by_box ON entry_jobs (box, created_at DESC);

-- The job's log, line by line, for the console to follow. Never secrets: the
-- password, relay identities and the hub key stay out of it by construction
-- (the runner writes its own lines, not raw remote output with env in it).
CREATE TABLE entry_job_log (
    job_id  uuid NOT NULL REFERENCES entry_jobs (id) ON DELETE CASCADE,
    seq     integer NOT NULL,
    at      timestamptz NOT NULL DEFAULT now(),
    line    text NOT NULL,
    PRIMARY KEY (job_id, seq)
);

-- Admission probes the hub asks of the vantages. A prober claims one, probes
-- the named relays from where it stands, and reports a verdict per relay.
CREATE TABLE entry_probe_tasks (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id      uuid REFERENCES entry_jobs (id) ON DELETE SET NULL,
    relays      text[] NOT NULL CHECK (cardinality(relays) > 0),
    status      text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'claimed', 'done')),
    -- The vantage that claimed it (its region label, e.g. ru-spb).
    claimed_by  text,
    -- {"<relay>": "<verdict>", …} as the prober classified them.
    verdicts    jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    claimed_at  timestamptz,
    done_at     timestamptz,
    CONSTRAINT entry_probe_tasks_done_has_verdicts CHECK ((status = 'done') = (verdicts IS NOT NULL))
);

CREATE INDEX entry_probe_tasks_queued ON entry_probe_tasks (created_at) WHERE status = 'queued';
