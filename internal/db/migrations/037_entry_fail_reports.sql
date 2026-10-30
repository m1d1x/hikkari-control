-- 037 — what clients report about relays that did not get through (roadmap
-- 42 П6.3).
--
-- Observations, and only that: a report never kills a relay or earns a
-- replacement (owner, 16.10 — death comes from our probes, П6.6, or from the
-- operator). Thresholds and quorums wait for data from real relays; these rows
-- are that data. A report is kept only for a relay the account was actually
-- handed, so the table cannot be filled with claims about the whole pool.
--
-- The cover tried, the error class and the reporter's IP are sealed in meta_ct
-- (cryptobox); account erasure clears them and keeps the bare row (relay,
-- stage, times), like entry_grants.

CREATE TABLE entry_fail_reports (
    id          bigserial PRIMARY KEY,
    account_id  uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    relay_id    bigint NOT NULL REFERENCES entry_relays(id) ON DELETE RESTRICT,
    stage       text NOT NULL CHECK (stage IN ('tcp', 'tls', 'reality', 'websocket', 'timeout')),
    -- The client's clock, clamped by the server to the week before receipt.
    observed_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    meta_ct     bytea
);

-- Per relay over time: the shape a probe or a model reads (П6.6, П6.7).
CREATE INDEX entry_fail_reports_by_relay ON entry_fail_reports (relay_id, received_at);

-- The repeat check: one row per (account, relay, stage) per hour.
CREATE INDEX entry_fail_reports_repeat ON entry_fail_reports (account_id, relay_id, stage, received_at);
