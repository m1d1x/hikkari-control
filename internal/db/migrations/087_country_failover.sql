ALTER TABLE entry_country_egress DROP COLUMN auto_port;
ALTER TABLE entry_country_egress ADD CHECK (nl_port <> de_port);
ALTER TABLE entry_country_egress ADD COLUMN down_country text CHECK (down_country IN ('NL', 'DE', 'none'));
ALTER TABLE entry_country_egress ADD COLUMN health_at timestamptz;
