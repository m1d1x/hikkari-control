-- 035 — Entry without a password (plan 76.2.4 §A1–§A4, §A13).
--
-- An account has no password any more: a device gets in by registering (a
-- code from the mailbox), by another device of the account (QR or a typed
-- code), or by the seed phrase. So:
--
--   * users.password_hash and auth_lockouts go — there is nothing to hash and
--     nothing to guess. The admin plane keeps its own passwords in `admins`;
--     it is a different table and is not touched.
--   * email_verifications loses the password_reset purpose.
--   * users.email_changed_at: when the address last changed. A contact younger
--     than thirty days does not count for recovery without the conversations
--     (§A7, task 76.13), or a thief who swapped both contacts would take the
--     account through it. NULL = never changed since registration.
--   * e2e_device_lists.identity_key: the key that signs the list, in the clear,
--     so sign_in_with_phrase finds the account without the server parsing the
--     list. One account per key.
--   * e2e_device_links is rebuilt for one kind of link: the new device opens
--     it and shows a QR and a six-character code, whose first half is the
--     address a typing device finds it by. Rows live ten minutes, so dropping
--     the table loses nothing meant to last.
--   * user_sessions.list_publish_pending: the one device-list publish a young
--     session may make (it adds itself). It used to live on the link row; a
--     device that signed in by phrase has no link, so the claim moves to the
--     session both kinds of newcomer have.
--
-- Stand data only (owner, 13.10): no data is migrated.

ALTER TABLE users DROP COLUMN password_hash;
ALTER TABLE users ADD COLUMN email_changed_at timestamptz;

DROP TABLE auth_lockouts;

DELETE FROM email_verifications WHERE purpose = 'password_reset';
ALTER TABLE email_verifications DROP CONSTRAINT email_verifications_purpose_check;
ALTER TABLE email_verifications ADD CONSTRAINT email_verifications_purpose_check
    CHECK (purpose IN ('registration', 'email_change'));

ALTER TABLE e2e_device_lists ADD COLUMN identity_key bytea
    CHECK (identity_key IS NULL OR octet_length(identity_key) = 65);
CREATE UNIQUE INDEX e2e_device_lists_identity_key ON e2e_device_lists (identity_key)
    WHERE identity_key IS NOT NULL;

ALTER TABLE user_sessions ADD COLUMN list_publish_pending boolean NOT NULL DEFAULT false;

DROP TABLE e2e_device_link_messages;
DROP TABLE e2e_device_links;

CREATE TABLE e2e_device_links (
    link_id           bytea PRIMARY KEY CHECK (octet_length(link_id) = 16),
    -- The first half of the shown code: three Crockford base32 characters.
    -- NULL for a screen that shows its QR alone: no free address was left.
    code_address      text CHECK (code_address ~ '^[0-9A-HJKMNP-TV-Z]{3}$'),
    -- The joined live device's account and session; NULL while unjoined, and
    -- again after a failed exchange hands the link back.
    account_id        uuid REFERENCES users(id) ON DELETE CASCADE,
    live_session_id   uuid,
    -- The anonymous new device's device-key binding, registered as a session
    -- when it accepts.
    new_public_key    bytea NOT NULL,
    new_fingerprint   bytea NOT NULL,
    new_platform      text,
    new_card_ct       bytea,
    new_ip_ct         bytea,
    -- 1 open, 2 approved by the live device, 3 burned, 4 closed, 5 accepted.
    state             smallint NOT NULL DEFAULT 1 CHECK (state BETWEEN 1 AND 5),
    failures          integer NOT NULL DEFAULT 0,
    issued_session_id uuid,
    created_at        timestamptz NOT NULL DEFAULT now()
);

-- An address names one open link. Expired open rows are swept before every
-- open, in the same transaction, so they never hold an address hostage.
CREATE UNIQUE INDEX e2e_device_links_code_address ON e2e_device_links (code_address)
    WHERE state = 1;

CREATE TABLE e2e_device_link_messages (
    seq        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    link_id    bytea NOT NULL REFERENCES e2e_device_links(link_id) ON DELETE CASCADE,
    -- 1 live, 2 new.
    from_role  smallint NOT NULL CHECK (from_role BETWEEN 1 AND 2),
    -- 1 exchange (QR secret), 2 hello, 3 root, 4 exchange (code secret).
    kind       smallint NOT NULL CHECK (kind BETWEEN 1 AND 4),
    body       bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX e2e_device_link_messages_link ON e2e_device_link_messages (link_id, seq);
