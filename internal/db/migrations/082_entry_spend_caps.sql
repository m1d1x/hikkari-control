-- 082 — money is the owner's one knob (plan 42 П9.19).
--
-- Owner's decision 28.10: the only thing the owner sets for the autopilot is
-- how much it may spend, per day and per month, and only per spend group —
-- the hosters together, the registrars together. Everything else the hub
-- derives, and whatever fits under the caps it does by itself, purchases
-- included.
--
-- What goes, and why:
--
--   entry_providers.unit_cap_cents, monthly_cap_cents — the caps were per
--       account, and a group of two registrars would have had two monthly
--       caps the owner has to add up. The caps are the group's now
--       (entry_spend_caps). The per-purchase cap goes without a replacement:
--       a day's cap bounds any one purchase, and a price cap in cents says
--       nothing true when prices move tenfold.
--   entry_detection_policy.fleet_orders_per_hour, fleet_orders_per_day — a
--       count of orders protects nothing money does not: a runaway order loop
--       is stopped by the day's money, and a real surge was stopped by the
--       count.
--   fleet_horizon_seconds — the horizon is always measured (twice the median
--       from an order to admission).
--   fleet_surplus_hold_seconds, entry_fleet_pools.surplus_since — a surplus
--       machine is handed back at the end of the hour already paid for; the
--       pool's forecast says whether it will be needed before a new one could
--       be admitted, which is what the hold was guessing at.
--   domain_pool_target — the free domains the pool keeps are derived from
--       how fast domains are consumed (internal/entry/domainpool.go).
--
-- Data: provider credentials, inventory, purchases and history stay in place.
-- Existing monthly caps are carried into their spend groups below; obsolete
-- count-based settings are intentionally removed.

CREATE TABLE entry_spend_caps (
    -- The spend group: every hosting account, or every registrar account. A
    -- group is code (the adapter's role), so a new provider joins its group
    -- without a migration.
    role              text PRIMARY KEY CHECK (role IN ('hosting', 'registrar')),
    daily_cap_cents   integer NOT NULL CHECK (daily_cap_cents >= 0),
    monthly_cap_cents integer NOT NULL CHECK (monthly_cap_cents >= 0),
    updated_by        uuid REFERENCES admins(id) ON DELETE SET NULL,
    updated_at        timestamptz NOT NULL DEFAULT now(),
    -- A day never allows more than the month does.
    CONSTRAINT entry_spend_caps_month_covers_day CHECK (monthly_cap_cents >= daily_cap_cents)
);

-- Small on purpose, as the old defaults were: the first thing an autopilot
-- with a card should be is cheap, and the owner raises the caps deliberately.
INSERT INTO entry_spend_caps (role, daily_cap_cents, monthly_cap_cents) VALUES
    ('hosting', 300, 6000),
    ('registrar', 500, 500);

-- Preserve configured providers' monthly allowances when adopting group caps.
-- These are the provider kinds supported before this migration. A fresh hub
-- keeps the defaults above; a configured zero budget remains zero.
UPDATE entry_spend_caps c
SET monthly_cap_cents = p.monthly_cents,
    daily_cap_cents = least(c.daily_cap_cents, p.monthly_cents)
FROM (
    SELECT CASE kind WHEN 'vultr' THEN 'hosting' ELSE 'registrar' END AS role,
           sum(monthly_cap_cents)::integer AS monthly_cents
    FROM entry_providers WHERE kind IN ('vultr', 'porkbun')
    GROUP BY CASE kind WHEN 'vultr' THEN 'hosting' ELSE 'registrar' END
) p
WHERE c.role = p.role;


ALTER TABLE entry_providers
    DROP CONSTRAINT entry_providers_caps,
    DROP COLUMN unit_cap_cents,
    DROP COLUMN monthly_cap_cents;

ALTER TABLE entry_detection_policy
    DROP CONSTRAINT entry_detection_policy_orders_day_covers_hour,
    DROP COLUMN fleet_horizon_seconds,
    DROP COLUMN fleet_orders_per_hour,
    DROP COLUMN fleet_orders_per_day,
    DROP COLUMN fleet_surplus_hold_seconds,
    DROP COLUMN domain_pool_target;

ALTER TABLE entry_fleet_pools DROP COLUMN surplus_since;

-- Whatever fits under the caps the hub does by itself: the purchases start in
-- `auto` now (playbooks.go), and an `ask` saved for them on the stand would
-- keep the old behaviour behind the owner's back. An `off` is a brake someone
-- pulled on purpose and stays.
DELETE FROM entry_playbook_modes
WHERE cause IN ('short_of_fleet', 'short_of_domains') AND mode = 'ask';
