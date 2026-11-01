-- Live log stream from installs in tester mode: the client ships its sanitized
-- engine log in chunks while the user has the tester switch on. Kept 7 days.
CREATE TABLE IF NOT EXISTS support_tester_log_chunks (
    id bigserial PRIMARY KEY,
    install_id text NOT NULL,
    platform text NOT NULL,
    version text NOT NULL,
    lines integer NOT NULL,
    dropped integer NOT NULL DEFAULT 0,
    body text NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS support_tester_log_chunks_install ON support_tester_log_chunks (install_id, received_at DESC);
CREATE INDEX IF NOT EXISTS support_tester_log_chunks_received ON support_tester_log_chunks (received_at);
