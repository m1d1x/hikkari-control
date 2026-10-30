-- 080 — the seed phrase is one of two factors (plan 76 §1.47–§1.54, 76.10 Ф2).
--
-- The phrase used to derive the root, and sign_in_with_phrase found the
-- account by the identity key published beside its device list. Now the root
-- is random and sealed here under a key from the phrase's half and a share the
-- server keeps; the share is released only for the right PIN, ten tries:
--
--   * e2e_phrase_vaults: one row per account.
--       sign_in_key  — the public key the phrase derives; the account is
--                      found by it, and only its signature spends a PIN try;
--       share        — the server's half of the key the root is sealed under;
--                      NULL once ten wrong PINs wiped it;
--       sealed_root  — the root under (phrase half, share); NULL with share;
--       phrase_box   — the phrase under a key the root derives, so any device
--                      of the account can show it;
--       pin_hash     — argon2id of the PIN with pin_salt;
--       failures     — wrong PINs since the last right one.
--     Neither the phrase nor the root is ever here in the clear, and the PIN
--     alone opens nothing: the server has no phrase half.
--   * e2e_device_lists.identity_key goes: nothing finds an account by it any
--     more.
--
-- Stand data only (owner, 27.10): no account gets a vault by migration; the
-- client creates one with the PIN (76.10 Ф3).

CREATE TABLE e2e_phrase_vaults (
    account_id   uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    sign_in_key  bytea NOT NULL UNIQUE CHECK (octet_length(sign_in_key) = 65),
    share        bytea CHECK (share IS NULL OR octet_length(share) = 32),
    sealed_root  bytea,
    phrase_box   bytea NOT NULL,
    pin_hash     bytea NOT NULL,
    pin_salt     bytea NOT NULL,
    failures     integer NOT NULL DEFAULT 0 CHECK (failures BETWEEN 0 AND 10),
    wiped_at     timestamptz,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CHECK ((share IS NULL) = (sealed_root IS NULL)),
    CHECK ((share IS NULL) = (wiped_at IS NOT NULL))
);

DROP INDEX e2e_device_lists_identity_key;
ALTER TABLE e2e_device_lists DROP COLUMN identity_key;
