-- The panel's charts the hub keeps computed (internal/admin/warm.go): every
-- chart URL an operator opened in the last week, by role, so the warmer picks
-- them up again after the hub restarts.
CREATE TABLE IF NOT EXISTS admin_warm_charts (
    role     text        NOT NULL,
    uri      text        NOT NULL,
    asked_at timestamptz NOT NULL,
    PRIMARY KEY (role, uri)
);
