-- What a tester's tunnel carried and ran on (owner, 18.11: «мало наблюдаемости
-- за тестерами … они добровольно дали разрешение на все»). The client (newer
-- than 1.0.92) writes in a tester's log, next to the downtime minutes:
--   "[downtime] bytes from=<unix> v=tu,td,du,dd;.;-" — per minute the bytes up
--   and down through the tunnel and past it, read off its open connections
--   every 5 s (a little short, never long);
--   "[downtime] state at=<unix> mode=proxy proto=hikkari profile="…"" — the
--   routing mode in effect, the active profile's protocol and name, when it
--   changes and with every run.
-- The hub moves both here (internal/downtime). Testers only: an ordinary user's
-- telemetry carries neither.
CREATE TABLE IF NOT EXISTS product_tester_traffic_minutes (
    install_id text NOT NULL,
    minute timestamptz NOT NULL,
    tunnel_up bigint NOT NULL CHECK (tunnel_up >= 0),
    tunnel_down bigint NOT NULL CHECK (tunnel_down >= 0),
    direct_up bigint NOT NULL CHECK (direct_up >= 0),
    direct_down bigint NOT NULL CHECK (direct_down >= 0),
    PRIMARY KEY (install_id, minute)
);

CREATE INDEX IF NOT EXISTS product_tester_traffic_minutes_minute ON product_tester_traffic_minutes (minute);

CREATE TABLE IF NOT EXISTS product_tester_state (
    install_id text NOT NULL,
    at timestamptz NOT NULL,
    mode text NOT NULL,
    proto text NOT NULL,
    profile text NOT NULL,
    PRIMARY KEY (install_id, at)
);
