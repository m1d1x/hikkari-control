-- Letters the hub sends (internal/mailer): queued inside the transaction of
-- the act that sends them, delivered by a worker with retries. The letter
-- may carry a subscription link, so it is sealed with the content key.
CREATE TABLE hub_mail_outbox (
    id bigserial PRIMARY KEY,
    -- the recipient, in the clear for rate limits and support lookups
    to_address text NOT NULL CHECK (length(to_address) <= 254),
    -- what the letter is for: rate limits count by it
    kind text NOT NULL CHECK (kind ~ '^[a-z_]{1,32}$'),
    letter_sealed bytea NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    -- when the worker may try it (next); a claimed letter is leased forward
    next_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    sent_at timestamptz,
    -- given up on after the last retry
    failed_at timestamptz,
    provider text,
    last_error text
);

CREATE INDEX hub_mail_outbox_due ON hub_mail_outbox (next_at) WHERE sent_at IS NULL AND failed_at IS NULL;
CREATE INDEX hub_mail_outbox_to ON hub_mail_outbox (to_address, kind, created_at DESC);
