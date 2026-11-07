-- Download measurements on owned RU points; no subscriber measurements.
CREATE TABLE relay_capacity (
    slot text NOT NULL REFERENCES entry_vantages(slot) ON DELETE CASCADE,
    config_key text NOT NULL CHECK (config_key ~ '^[0-9a-f]{64}$'),
    measured_at timestamptz NOT NULL,
    sample jsonb NOT NULL,
    core text NOT NULL,
    PRIMARY KEY (slot, config_key)
);
CREATE INDEX relay_capacity_time ON relay_capacity(measured_at);

CREATE TABLE relay_capacity_settings (
    id boolean PRIMARY KEY DEFAULT true CHECK(id),
    enabled boolean NOT NULL DEFAULT true,
    interval_minutes integer NOT NULL DEFAULT 30 CHECK(interval_minutes BETWEEN 10 AND 1440)
);
INSERT INTO relay_capacity_settings(id) VALUES(true);
