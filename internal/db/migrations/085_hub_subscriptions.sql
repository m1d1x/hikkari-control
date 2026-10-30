-- Hikkari subscriptions: a random token per subscriber, tied to the entry-pool
-- account its relays are granted to. Only the token's SHA-256 is stored, so
-- the table gives no working link away.
CREATE TABLE hub_subscriptions (
    token_hash bytea PRIMARY KEY CHECK (length(token_hash) = 32),
    account_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz
);
CREATE INDEX hub_subscriptions_account ON hub_subscriptions(account_id);
