-- The voice dataset (hikkari-client plans/research/2026-11-08-udp-abroad-from-testers.md):
-- one row per [diag:voice] series a tester's client ran. A series is what a
-- call sends, a 160-byte datagram every 20 ms for 30 s, through the tunnel
-- (path 'tun', on whatever profile was selected) and, on Windows, from the
-- physical card past it (path 'direct': the tester's own network). It answers
-- whether calls through TCP profiles suffer on testers' networks, the question
-- that decides whether Hikkari gets a UDP leg beside its TCP profiles. Kept for
-- good, past the week the log stream keeps.
--
-- No address and no secret: a profile is its tag, a network its AS number.
CREATE TABLE IF NOT EXISTS voice_runs (
    id bigserial PRIMARY KEY,
    -- install | the client's line time | path: the same line read twice is one row.
    source_key text NOT NULL UNIQUE,
    install_id text NOT NULL,
    measured_at timestamptz NOT NULL,
    client_version text NOT NULL DEFAULT '',
    on_request boolean NOT NULL DEFAULT false, -- probe_voice, not the periodic round
    path text NOT NULL CHECK (path IN ('tun', 'direct')),
    profile text NOT NULL DEFAULT '', -- the selected profile's tag (path tun)
    proto text NOT NULL DEFAULT '',
    iface text NOT NULL DEFAULT '', -- the card (path direct)
    network text NOT NULL DEFAULT '', -- the tester's ISP as AS number, 'unknown' when not known
    network_at text NOT NULL DEFAULT '', -- 'card' (now, Windows) or 'baseline' (tunnel-down snapshot)
    mobile boolean NOT NULL DEFAULT false,
    sent integer NOT NULL DEFAULT 0,
    recv integer NOT NULL DEFAULT 0,
    loss_run integer NOT NULL DEFAULT 0,
    rtt_p50_ms integer,
    rtt_p95_ms integer,
    rtt_max_ms integer,
    jitter_ms real,
    gaps60 integer NOT NULL DEFAULT 0,
    gaps150 integer NOT NULL DEFAULT 0,
    gap_max_ms integer,
    reorder integer NOT NULL DEFAULT 0,
    dup integer NOT NULL DEFAULT 0,
    error text NOT NULL DEFAULT '',
    received_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS voice_runs_time ON voice_runs (measured_at DESC);
CREATE INDEX IF NOT EXISTS voice_runs_install_time ON voice_runs (install_id, measured_at DESC);
-- The ingester's place in support_tester_log_chunks is a relay_runs_cursor row
-- named 'tester_voice' (022): one table holds every reader's place in the stream.
