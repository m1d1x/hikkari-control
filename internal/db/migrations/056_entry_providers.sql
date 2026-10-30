-- 056 — the hub's provider accounts: credentials and limits the owner edits in
-- the console (roadmap 42 П9.1).
--
-- Until now the registrar keys (PORKBUN_*) and the domain budget
-- (ENTRY_DOMAIN_*) were environment variables: changing a key or a cap meant a
-- redeploy, and the console could not say whether a key worked or how much
-- credit was left. This table is where the hub keeps one account per provider
-- kind instead, edited in the console and applied without a restart. The
-- environment variables are gone; a hub with no row here is pool-only, as a hub
-- without the variables was.
--
-- One row per kind: a kind is one adapter in internal/entrywire (porkbun now,
-- a hosting provider in П9.2). Which kinds exist, their role (registrar,
-- hosting) and the names of their credential fields are code, not data — they
-- come with the adapter, so a new adapter needs no migration; the hub refuses
-- to save a kind it has no adapter for.
--
-- Credentials are sealed under the server key (cryptobox) as one JSON object of
-- field → value and never leave the hub: the console sees only creds_hint, a
-- masked tail of the public key, and whether the last check went through.
--
-- The caps are the hub's half of the budget gate (the provider's own monthly
-- limit, read on every check, is the other half): unit_cap_cents is the most
-- one purchase may cost, monthly_cap_cents the most the hub spends with this
-- provider in a calendar month. tlds is the registrar's candidate list, tried
-- in order; empty means the built-in list.

CREATE TABLE entry_providers (
    kind              text PRIMARY KEY CHECK (kind ~ '^[a-z][a-z0-9]*$'),
    enabled           boolean NOT NULL DEFAULT false,
    creds_ct          bytea,
    creds_hint        text NOT NULL DEFAULT '',
    unit_cap_cents    integer NOT NULL DEFAULT 200 CHECK (unit_cap_cents >= 0),
    monthly_cap_cents integer NOT NULL DEFAULT 500 CHECK (monthly_cap_cents >= 0),
    tlds              text[] NOT NULL DEFAULT '{}',
    -- The last check of the account (credentials, credit, the provider's own
    -- monthly limit), so the console shows it without calling out on every
    -- page load. NULL until the first check.
    checked_at          timestamptz,
    check_ok            boolean,
    check_error         text NOT NULL DEFAULT '',
    balance_cents       integer,
    account_spent_cents integer,
    account_limit_cents integer,
    updated_by        uuid REFERENCES admins(id) ON DELETE SET NULL,
    updated_at        timestamptz NOT NULL DEFAULT now(),
    -- An enabled provider is one the hub will call; it must have something to
    -- call with.
    CONSTRAINT entry_providers_enabled_has_creds CHECK (NOT enabled OR creds_ct IS NOT NULL),
    CONSTRAINT entry_providers_caps CHECK (monthly_cap_cents >= unit_cap_cents)
);
