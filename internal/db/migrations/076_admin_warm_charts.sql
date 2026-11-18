-- The panel's charts the hub keeps computed (internal/admin/warm.go). Every
-- chart in every window is computed always; this table holds the URLs drawn
-- for one entry, knob or profile that an operator opened in the last 30 days,
-- by role, so the warmer picks them up again after the hub restarts.
CREATE TABLE IF NOT EXISTS admin_warm_charts (
    role     text        NOT NULL,
    uri      text        NOT NULL,
    asked_at timestamptz NOT NULL,
    PRIMARY KEY (role, uri)
);
