-- Bounded observation history; latest failures still replace latest successes.
CREATE TABLE relay_capacity_history (
 slot text NOT NULL REFERENCES entry_vantages(slot) ON DELETE CASCADE,
 config_key text NOT NULL CHECK(config_key ~ '^[0-9a-f]{64}$'),
 measured_at timestamptz NOT NULL,
 sample jsonb NOT NULL,
 PRIMARY KEY(slot,config_key,measured_at)
);
CREATE INDEX relay_capacity_history_time ON relay_capacity_history(measured_at);
INSERT INTO relay_capacity_history(slot,config_key,measured_at,sample)
 SELECT slot,config_key,measured_at,sample FROM relay_capacity WHERE measured_at>now()-interval '24 hours';
