-- 020 — E2E account keys (roadmap 76.1): what the server keeps for an account's
-- end-to-end keys, and why none of it is readable here.
--
--   * e2e_device_lists — the account's device list, SIGNED by the account
--     identity key. The server cannot forge a version; it only orders them.
--   * e2e_keyrings     — the account keyring, SEALED under the account history
--     key. The server cannot open it.
--   * e2e_device_links — a short-lived mailbox: the root secret HPKE-sealed to
--     a key that reached the live device by QR. The server cannot open it, and
--     a row lives minutes and is deleted on the one read.
--
-- All three are blobs plus the metadata the server needs to order and scope
-- them (account, version, time). Each is ABOUT the account, so EraseAccount
-- deletes them (internal/auth/account_delete.go, eraseSteps).
--
-- New tables only: no existing data is touched.

CREATE TABLE e2e_device_lists (
    account_id  uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    version     bigint NOT NULL CHECK (version > 0),
    signed_list bytea NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE e2e_keyrings (
    account_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    version    bigint NOT NULL CHECK (version > 0),
    sealed     bytea NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE e2e_device_links (
    account_id  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    link_id     bytea NOT NULL CHECK (octet_length(link_id) = 16),
    sealed_root bytea NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, link_id)
);
