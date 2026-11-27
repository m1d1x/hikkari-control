-- The hosters as accounts, not just as a string on a machine (19.11: на экране
-- «Машины» нужен учёт хостера и его оплаты). entry_boxes.provider is the kind
-- the hub's API keys go by; this table is what the owner needs to open the
-- hoster's billing page and pay: a readable name, the ASN the machine's subnet
-- belongs to, the billing URL, the console login, and the API key when the
-- hoster has one.
--
-- Secrets live sealed, the way every other secret in the hub does
-- (internal/cryptobox): the screen shows hasLogin/hasPassword/hasApiKey, and
-- reading one back is an audited action, not a list field.
--
-- A row may exist for a hoster the hub never bought from (the owner is just
-- writing down where to pay) and a hoster may have machines and no row; the
-- console shows the union and counts machines from entry_boxes, so neither
-- side makes the other invisible.
--
-- spend_cap_cents is a note for the owner, not a gate: money is stopped by the
-- hub's own budgets (entry_budgets), which act. NULL means "не задан".
CREATE TABLE IF NOT EXISTS entry_provider_accounts (
    provider        text PRIMARY KEY CHECK (provider ~ '^[a-z][a-z0-9]*$'),
    display_name    text NOT NULL DEFAULT '',
    asn             text NOT NULL DEFAULT '',
    billing_url     text NOT NULL DEFAULT '',
    login_ct        bytea,
    password_ct     bytea,
    api_host        text NOT NULL DEFAULT '',
    api_key_ct      bytea,
    spend_cap_cents integer CHECK (spend_cap_cents IS NULL OR spend_cap_cents >= 0),
    note            text NOT NULL DEFAULT '',
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- Terabytes a month a machine's tariff includes, per machine (the provider-wide
-- figure is entry_provider_plans, 077). NULL: not known — a machine bought by
-- hand or before the owner wrote it down. The machines screen shows it next to
-- the traffic the relays actually carried.
ALTER TABLE entry_boxes ADD COLUMN IF NOT EXISTS traffic_quota_gb integer;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'entry_boxes_traffic_quota_check'
    ) THEN
        ALTER TABLE entry_boxes
            ADD CONSTRAINT entry_boxes_traffic_quota_check
            CHECK (traffic_quota_gb IS NULL OR traffic_quota_gb >= 0);
    END IF;
END $$;
