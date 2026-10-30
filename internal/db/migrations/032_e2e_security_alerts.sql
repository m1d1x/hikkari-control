-- 032 — a client's refusal of a forged key list becomes an alert for the owner
-- (task 76.2′, pivot §1.31).
--
-- The client pins a peer's identity key on first contact and silently ignores
-- any device list the server serves that the pin refuses — the person sees
-- nothing, by the owner's decision. What it does is log one line under the
-- `e2e.security` target, and that line reaches the server on the log channel.
-- This table is where it stops being a log line: one row per
-- (observer, peer, reason), so a client that hits the same forged list on every
-- sync raises one alert, not thousands.
--
-- Here rather than read back out of ClickHouse: the log table keeps 14 days, and
-- a sign that the server served a forged key list must not expire unread.
--
-- No foreign keys. `observer_id` is the authenticated account that sent the
-- line; `peer_id` is a claim inside it, and a client may name any id — a key it
-- cannot satisfy would refuse the whole log batch, and the client would resend
-- it forever. Erasure cleans both sides (auth eraseSteps).
CREATE TABLE e2e_security_alerts (
    id              bigserial PRIMARY KEY,
    observer_id     uuid NOT NULL,
    peer_id         uuid NOT NULL,
    reason          text NOT NULL,
    severity        text NOT NULL CHECK (severity IN ('warning', 'critical')),
    -- From the newest occurrence: the fingerprint the observer has pinned, the
    -- list version it holds, and what verification said.
    pinned          text NOT NULL DEFAULT '',
    held_version    bigint NOT NULL DEFAULT 0,
    detail          text NOT NULL DEFAULT '',
    first_seen      timestamp with time zone NOT NULL,
    last_seen       timestamp with time zone NOT NULL,
    occurrences     bigint NOT NULL DEFAULT 1,
    -- An acknowledged alert reopens when it happens again after the
    -- acknowledgement: a forgery that recurs is news.
    acknowledged_at timestamp with time zone,
    acknowledged_by uuid,
    UNIQUE (observer_id, peer_id, reason)
);
CREATE INDEX e2e_security_alerts_open_idx ON e2e_security_alerts (last_seen DESC)
    WHERE acknowledged_at IS NULL;
CREATE INDEX e2e_security_alerts_peer_idx ON e2e_security_alerts (peer_id);
