-- Billing (hikkari-client plans/2026-10-31-subscriptions.md, «Оплата и
-- автовыдача»). An order is one purchase of a plan: a new subscription or the
-- next period of an existing one. It is settled by the payment provider's
-- event; the subscription is issued or renewed in the same transaction that
-- marks the order paid, so a payment is never taken without access given.

-- What the landing shows a plan as («1 месяц»).
ALTER TABLE hub_plans ADD COLUMN title text NOT NULL DEFAULT '';

-- The buyer's address: the receipt, and the way back to a lost link.
ALTER TABLE hub_accounts
    ADD COLUMN email text CHECK (email IS NULL OR (length(email) <= 254 AND email ~ '^[^@\s]+@[^@\s]+$'));

-- The token sealed with the content key (internal/cryptobox): the link is
-- shown to the buyer after payment and can be sent to them again. Lookups
-- still go by token_hash only. NULL for subscriptions issued before billing.
ALTER TABLE hub_subscriptions ADD COLUMN token_sealed bytea;

CREATE TABLE hub_orders (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- SHA-256 of the order's secret: the buyer's browser proves with it that
    -- the order is theirs and reads the link once it is paid
    secret_hash bytea NOT NULL CHECK (length(secret_hash) = 32),
    plan_id text NOT NULL REFERENCES hub_plans(id) ON DELETE RESTRICT,
    -- the price when ordered, in kopecks; the provider's event must match it
    amount_cents integer NOT NULL CHECK (amount_cents > 0),
    email text NOT NULL CHECK (length(email) <= 254 AND email ~ '^[^@\s]+@[^@\s]+$'),
    -- a renewal names its account from the start; a new subscription gets
    -- its account when paid
    renewal boolean NOT NULL,
    account_id uuid REFERENCES hub_accounts(id) ON DELETE SET NULL,
    -- the provider that takes the payment and its id for it
    provider text NOT NULL CHECK (provider ~ '^[a-z0-9]{1,32}$'),
    provider_ref text,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'paid', 'canceled', 'expired')),
    created_at timestamptz NOT NULL DEFAULT now(),
    -- the last time the hub asked the provider where the payment stands
    checked_at timestamptz,
    closed_at timestamptz,
    CHECK ((status = 'pending') = (closed_at IS NULL))
);

CREATE UNIQUE INDEX hub_orders_provider_ref ON hub_orders (provider, provider_ref) WHERE provider_ref IS NOT NULL;
CREATE INDEX hub_orders_pending ON hub_orders (created_at) WHERE status = 'pending';
CREATE INDEX hub_orders_account ON hub_orders (account_id, created_at DESC);
