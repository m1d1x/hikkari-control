-- Plans and the terms of a subscription (hikkari-client
-- plans/2026-10-31-subscriptions.md). A plan is what is sold: how long, how
-- much traffic, at what price. A subscription carries the terms it was sold
-- on, copied from the plan, so editing a plan never changes what someone
-- already paid for.
CREATE TABLE hub_plans (
    id text PRIMARY KEY CHECK (id ~ '^[a-z0-9][a-z0-9-]{0,31}$'),
    days integer NOT NULL CHECK (days > 0),
    -- 0: no traffic limit
    traffic_bytes bigint NOT NULL CHECK (traffic_bytes >= 0),
    price_cents integer NOT NULL CHECK (price_cents >= 0),
    -- a retired plan is no longer sold; subscriptions on it keep their terms
    retired_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Terms of the period a subscription is in. Subscriptions issued before plans
-- existed have no end and no limit (NULL / 0) until renewed onto a plan.
ALTER TABLE hub_subscriptions
    ADD COLUMN plan_id text REFERENCES hub_plans(id) ON DELETE RESTRICT,
    ADD COLUMN expires_at timestamptz,
    ADD COLUMN traffic_bytes bigint NOT NULL DEFAULT 0 CHECK (traffic_bytes >= 0),
    -- the account's usage (hub_key_usage) when the period began: the period's
    -- use is the total since, so a renewal starts the counter afresh without
    -- touching the per-relay rows
    ADD COLUMN baseline_up bigint NOT NULL DEFAULT 0 CHECK (baseline_up >= 0),
    ADD COLUMN baseline_down bigint NOT NULL DEFAULT 0 CHECK (baseline_down >= 0);
