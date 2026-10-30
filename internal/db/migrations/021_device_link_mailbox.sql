-- 021 — device linking becomes «sign in by QR» (roadmap 76.1, plan 76 §3.5).
--
-- 020's e2e_device_links was a one-shot drop box: the live device posted the
-- root HPKE-sealed to a key the new device showed by QR, the new device took
-- it. That design only worked with the NEW device showing the QR. §3.5 needs
-- the QR in both directions, a 6-digit code without a camera, a new device
-- that is not signed in yet, and attempt limits — so a link becomes a small
-- mailbox both devices talk through (SPAKE2 messages, the hello, the sealed
-- root), plus the facts the server needs to rule on it.
--
-- Rows of the old table lived at most ten minutes and were deleted on read, so
-- dropping it loses nothing that was meant to last.
--
-- What the server can read here: who is linking to which account, from which
-- address, and when. That is metadata (plan §2.5). The mailbox bodies are
-- SPAKE2 messages and AES-GCM ciphertext under the SPAKE2 key; the server
-- holds neither the QR secret nor the code, so it cannot finish the exchange.

DROP TABLE e2e_device_links;

CREATE TABLE e2e_device_links (
    link_id          bytea PRIMARY KEY CHECK (octet_length(link_id) = 16),
    -- 1 QR shown by the new device, 2 QR shown by the live device, 3 code.
    kind             smallint NOT NULL CHECK (kind BETWEEN 1 AND 3),
    -- NULL only for kind 1 until the live device scans the QR.
    account_id       uuid REFERENCES users(id) ON DELETE CASCADE,
    -- The live device's session: the only one that may approve or fail it.
    live_session_id  uuid,
    -- Code path: the new device is already signed in by password.
    new_session_id   uuid,
    -- QR paths: the anonymous new device's device-key binding, registered as a
    -- session when the live device approves.
    new_public_key   bytea,
    new_fingerprint  bytea,
    new_platform     text,
    new_ip           text,
    joined           boolean NOT NULL DEFAULT false,
    -- 1 open, 2 approved, 3 burned, 4 closed.
    state            smallint NOT NULL DEFAULT 1 CHECK (state BETWEEN 1 AND 4),
    failures         integer NOT NULL DEFAULT 0,
    -- QR paths: the session issued to the new device on approval.
    issued_session_id uuid,
    -- The new device may publish the device list once while young (it adds
    -- itself); this records that it has.
    list_published   boolean NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX e2e_device_links_account ON e2e_device_links (account_id, created_at);

CREATE TABLE e2e_device_link_messages (
    seq        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    link_id    bytea NOT NULL REFERENCES e2e_device_links(link_id) ON DELETE CASCADE,
    -- 1 live, 2 new.
    from_role  smallint NOT NULL CHECK (from_role BETWEEN 1 AND 2),
    -- 1 exchange, 2 hello, 3 root.
    kind       smallint NOT NULL CHECK (kind BETWEEN 1 AND 3),
    body       bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX e2e_device_link_messages_link ON e2e_device_link_messages (link_id, seq);

-- Failed or abandoned links in a row per account; five pause linking for an
-- hour (plan §3.5). A successful link resets the run.
CREATE TABLE e2e_link_lockouts (
    account_id   uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    consecutive  integer NOT NULL DEFAULT 0,
    paused_until timestamptz
);
