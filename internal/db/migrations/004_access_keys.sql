-- Access keys (hikkari-client plans/2026-10-31-subscriptions.md). Every
-- account dials the fleet with credentials of its own, random and tied to
-- nothing a relay could read back to a person: a relay learns the keys of the
-- accounts granted to it and nothing else. That is what lets a subscription be
-- cut off on the relays and its traffic be counted across every relay it used.
CREATE TABLE hub_access_keys (
    account_id uuid PRIMARY KEY REFERENCES hub_accounts(id) ON DELETE CASCADE,
    -- the VLESS user id of Reality and XHTTP relays
    vless_id uuid NOT NULL UNIQUE,
    -- the Hysteria2 auth string
    hy2_auth text NOT NULL UNIQUE CHECK (hy2_auth ~ '^[0-9a-f]{32}$'),
    created_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO hub_access_keys (account_id, vless_id, hy2_auth)
SELECT id, gen_random_uuid(), replace(gen_random_uuid()::text, '-', '') FROM hub_accounts;

-- What an account's key carried through one relay during one run of its edge.
-- The edge reports totals since its process started (a lost pulse costs
-- nothing); boot names that run, so a restart opens a fresh row instead of
-- reading as negative traffic. An account's use is the sum of its rows.
CREATE TABLE hub_key_usage (
    account_id uuid NOT NULL REFERENCES hub_accounts(id) ON DELETE CASCADE,
    relay text NOT NULL,
    boot text NOT NULL CHECK (boot ~ '^[0-9a-f]{16,64}$'),
    bytes_up bigint NOT NULL DEFAULT 0 CHECK (bytes_up >= 0),
    bytes_down bigint NOT NULL DEFAULT 0 CHECK (bytes_down >= 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, relay, boot)
);
