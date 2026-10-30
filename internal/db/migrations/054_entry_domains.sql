-- 054 — the hub's own domains: a pool to draw from and a purchase journal
-- (roadmap 42 П4в Ф6).
--
-- A relay's domain is a consumable: blocked by name (domain_blocked, 052), it
-- is burned and replaced without a human. Domains come from two places — a
-- pool of names the hub already owns and has not yet put on a box, and, when
-- that pool is dry, a fresh purchase through the registrar. This table is
-- both: the owner's manually-bought seed names go in as source='seeded', every
-- API purchase adds a row, a domain on a box is 'assigned', and a domain that
-- died with its box is 'burned' and never drawn again.
--
-- Lifecycle: 'buying' (intent persisted with its idempotency key before the
-- charge, so a hub crash mid-purchase is recoverable — the key replays the
-- registrar's answer for 24h) → 'owned' (settled, free in the pool) →
-- 'assigned' (on a box) → 'burned'. A purchase that did not settle is 'failed'
-- and kept for audit, never drawn.
--
-- The name is plain here, unlike on the box (052, sealed): this is the hub's
-- private registrar ledger, queried by TLD and status to pick the next free
-- name and to total the month's spend for the budget gate. It never leaves the
-- hub, so it is not the gift to a censor that a plain list of live addresses
-- would be.

CREATE TABLE entry_domains (
    domain          text PRIMARY KEY,
    tld             text NOT NULL,
    status          text NOT NULL CHECK (status IN ('buying', 'owned', 'assigned', 'burned', 'failed')),
    box             text REFERENCES entry_boxes(name) ON DELETE SET NULL,
    source          text NOT NULL CHECK (source IN ('purchased', 'seeded')),
    cost_cents      integer NOT NULL DEFAULT 0 CHECK (cost_cents >= 0),
    order_id        text NOT NULL DEFAULT '',
    idempotency_key text NOT NULL DEFAULT '',
    error           text NOT NULL DEFAULT '',
    bought_at       timestamptz,
    assigned_at     timestamptz,
    burned_at       timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- One idempotency key is one purchase attempt: a unique partial index turns a
-- double-insert of the same key into a conflict, not a second charge.
CREATE UNIQUE INDEX entry_domains_idem ON entry_domains (idempotency_key) WHERE idempotency_key <> '';

-- The pool pick: free owned names, by TLD.
CREATE INDEX entry_domains_pool ON entry_domains (tld) WHERE status = 'owned';

-- The month's spend for the budget gate: reserved the moment a purchase
-- intent is written, so two concurrent buys cannot both slip under the cap.
CREATE INDEX entry_domains_spend ON entry_domains (created_at)
    WHERE source = 'purchased' AND status <> 'failed';

-- An XHTTP box no longer needs its domain at birth: the hub assigns one during
-- provisioning (from the pool or a purchase) and swaps it when the old one is
-- blocked. The invariant kept is only the other half — a box that is not XHTTP
-- never carries a domain.
ALTER TABLE entry_boxes DROP CONSTRAINT entry_boxes_xhttp_has_domain;
ALTER TABLE entry_boxes
    ADD CONSTRAINT entry_boxes_domain_only_xhttp
        CHECK (proto = 'vless-xhttp' OR domain_ct IS NULL);
