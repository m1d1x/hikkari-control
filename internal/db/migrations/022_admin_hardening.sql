-- 022 — the admin panel stops being one password away from the product
-- (roadmap task 77).
--
-- Before this file an operator was a login and a password: no second factor,
-- no roles, an erasure that ran the instant the button was pressed, and an
-- audit trail the acting process could rewrite. The network half of the fix
-- (the panel on a loopback listener behind `ssh -L`) needs no schema; this is
-- the rest of it.
--
-- Data: every row in `admins` today is a test operator created by the owner.
-- They all become `owner` — a role column that left the existing operator
-- unable to erase or administer would lock the owner out of their own panel.

-- ---------------------------------------------------------------------------
-- 77.4 Roles.
-- ---------------------------------------------------------------------------
--
-- Two, deliberately. `owner` may do everything; `support` reads, answers
-- tickets and works reports, and may not erase accounts, ban, or change the
-- platform's configuration. New operators start as `support`: the role that can
-- do less is the one you get by forgetting to choose.
ALTER TABLE admins
    ADD COLUMN role text NOT NULL DEFAULT 'support'
        CHECK (role IN ('owner', 'support'));
UPDATE admins SET role = 'owner';

-- The TOTP columns 007 laid down ahead of a flow that was never written. The
-- second factor is a passkey (below); a TOTP secret would be a second, weaker
-- way past the same gate.
ALTER TABLE admins DROP COLUMN totp_secret_ct;
ALTER TABLE admins DROP COLUMN totp_enrolled_at;

-- The one-time code that lets an operator register their FIRST passkey. Issued
-- only by the CLI on the box (`admin-create`, `admin-2fa-reset`), so enrolling
-- a second factor requires shell access — a stolen password alone cannot
-- enrol the thief's own key. Stored as a hash; expires.
ALTER TABLE admins ADD COLUMN enroll_code_hash bytea;
ALTER TABLE admins ADD COLUMN enroll_code_expires_at timestamp with time zone;

-- ---------------------------------------------------------------------------
-- 77.2 Second factor: passkeys and recovery codes.
-- ---------------------------------------------------------------------------

-- `credential` is the library's whole credential record (go-webauthn
-- webauthn.Credential) as JSON: public key, sign counter, backup flags,
-- transports. Kept whole rather than split into columns because every field of
-- it is read back into the next ceremony, and a column the code forgets to
-- read back is a silently weakened verification (the sign counter is exactly
-- that kind of field). `credential_id` is lifted out for the unique index.
CREATE TABLE admin_passkeys (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    admin_id      uuid NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    credential_id bytea NOT NULL UNIQUE,
    credential    jsonb NOT NULL,
    name          text NOT NULL DEFAULT '',
    created_at    timestamp with time zone NOT NULL DEFAULT now(),
    last_used_at  timestamp with time zone
);
CREATE INDEX admin_passkeys_admin_idx ON admin_passkeys (admin_id);

-- Ten one-time codes shown once, at enrolment or regeneration. The «seed
-- phrase» of the admin plane: the way back in when every device holding a
-- passkey is gone. Each code carries ~60 random bits, so a plain SHA-256 is
-- the right hash — a slow KDF protects guessable secrets, and these are not.
CREATE TABLE admin_recovery_codes (
    admin_id  uuid NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    code_hash bytea NOT NULL,
    used_at   timestamp with time zone,
    PRIMARY KEY (admin_id, code_hash)
);

-- A WebAuthn ceremony in flight: the challenge the server issued and what it
-- must remember until the browser answers. One row per ceremony, read once,
-- five minutes to live.
--
--   login   — the password was right; the second factor is pending. Holds the
--             device binding the session will be minted with, so nothing the
--             browser resends at the second step can change which device the
--             session belongs to.
--   enroll  — the same, for an operator with no passkey yet: the console code
--             and then a registration stand between them and a session.
--   step_up — a live session asking for a fresh touch before a destructive
--             action.
--   add_key — a live session registering another passkey.
CREATE TABLE admin_ceremonies (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    admin_id   uuid NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    purpose    text NOT NULL CHECK (purpose IN ('login', 'enroll', 'step_up', 'add_key')),
    -- step_up / add_key are bound to the session that asked; a ceremony begun
    -- by one session cannot be finished by another.
    session_id uuid REFERENCES admin_sessions(id) ON DELETE CASCADE,
    data       jsonb NOT NULL,
    expires_at timestamp with time zone NOT NULL
);
CREATE INDEX admin_ceremonies_expires_idx ON admin_ceremonies (expires_at);

-- ---------------------------------------------------------------------------
-- 77.3 Step-up: a fresh touch, not just a live cookie.
-- ---------------------------------------------------------------------------
--
-- A destructive action requires this to be in the future. One touch opens a
-- short window rather than prompting on every click: an operator working a
-- queue of reports would otherwise learn to touch the sensor without reading.
ALTER TABLE admin_sessions ADD COLUMN stepped_up_until timestamp with time zone;

-- ---------------------------------------------------------------------------
-- 77.3 Erasure from the panel waits like self-deletion does.
-- ---------------------------------------------------------------------------
--
-- A separate schedule from deletion_requested_at, on purpose: the person can
-- cancel their OWN deletion from any signed-in device without a password
-- (internal/account CancelDeletion), and an erasure staff ordered — a spam
-- bot, a legal request — must not be undone by the account it targets. Only
-- the panel cancels this one.
--
-- The account is banned and signed out for the duration, and the ban it had
-- before is remembered, so cancelling restores exactly the prior state rather
-- than unbanning someone who was banned for another reason.
ALTER TABLE users ADD COLUMN admin_erasure_at timestamp with time zone;
ALTER TABLE users ADD COLUMN admin_erasure_by uuid REFERENCES admins(id) ON DELETE SET NULL;
ALTER TABLE users ADD COLUMN admin_erasure_was_banned boolean NOT NULL DEFAULT false;
CREATE INDEX users_admin_erasure_idx ON users (admin_erasure_at) WHERE admin_erasure_at IS NOT NULL;

-- ---------------------------------------------------------------------------
-- 77.5 The audit trail can only grow.
-- ---------------------------------------------------------------------------
--
-- REVOKE would not do it: the server connects as the role that owns the table,
-- and an owner can grant itself back whatever it was refused. A trigger binds
-- the owner too; removing it takes DDL, which the application never issues
-- (tools/sqlguard keeps runtime data out of query text, so an injection cannot
-- smuggle it in).
--
-- The one legitimate rewrite is ON DELETE SET NULL from admins: an operator is
-- never deleted (admin-disable exists for that), so it is refused as well.
CREATE FUNCTION admin_audit_log_append_only() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'admin_audit_log is append-only (% refused)', TG_OP
        USING ERRCODE = 'insufficient_privilege';
END;
$$;

CREATE TRIGGER admin_audit_log_no_update_delete
    BEFORE UPDATE OR DELETE ON admin_audit_log
    FOR EACH ROW EXECUTE FUNCTION admin_audit_log_append_only();

CREATE TRIGGER admin_audit_log_no_truncate
    BEFORE TRUNCATE ON admin_audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION admin_audit_log_append_only();

-- What each operator has already seen of other operators' destructive actions,
-- so the panel can show the owner what happened since they last looked
-- (77.5 alert), without a second table that could drift from the log.
ALTER TABLE admins ADD COLUMN audit_seen_id bigint NOT NULL DEFAULT 0;
