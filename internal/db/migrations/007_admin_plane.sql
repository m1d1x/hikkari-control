-- 007 — an administrator stops being a role on a chat account.
--
-- Until now "admin" was a value in users.role plus a row in admin_credentials
-- hanging off users.id, and the access token the panel carried was minted by
-- the same key, with the same claims, as any chat client's. Three consequences,
-- all of them doors rather than walls:
--
--   * A leaked panel token was a working chat token for that person's account,
--     and a leaked chat token of anyone whose role happened to say 'admin' was
--     a working panel token. One credential, two blast radii.
--   * Everything that can reach a chat account — a password reset, a stolen
--     session, a device added to it — reached the admin surface behind it.
--   * "Who did this" in the audit trail was a user id, so revoking someone's
--     staff access and deleting their account were the same operation.
--
-- This migration gives the admin plane its own subject. `admins` has its own
-- primary key and NO foreign key into users: an administrator is not a
-- participant of the product, and cannot sign into it with these credentials.
-- users.role is left alone by this file but stops meaning anything to the admin
-- gate (internal/admin/middleware.go).

-- ---------------------------------------------------------------------------
-- The subject.
-- ---------------------------------------------------------------------------

-- `login` is separate from any chat username on purpose: the two namespaces
-- must be free to collide without implying a relationship.
--
-- The TOTP columns are laid down now and left unread. A second factor is the
-- next thing this table wants, and adding the columns later would mean a
-- migration against rows that exist; adding them now costs two nullable
-- columns. NULL secret = not enrolled, which is also the state every row starts
-- in, so nothing has to interpret "enrolled but unusable".
--
-- The secret is stored as ciphertext (bytea, cryptobox envelope) rather than
-- text: a TOTP secret is a bearer credential, and a database dump that hands
-- over every second factor defeats the point of having one.
CREATE TABLE IF NOT EXISTS admins (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    login            text NOT NULL,
    password_hash    text NOT NULL,
    display_name     text NOT NULL DEFAULT '',
    -- Disabling is not deletion: audit rows must keep pointing at a real row
    -- naming who acted, long after that person has lost access.
    is_disabled      boolean NOT NULL DEFAULT false,
    totp_secret_ct   bytea,
    totp_enrolled_at timestamp with time zone,
    created_at       timestamp with time zone NOT NULL DEFAULT now(),
    updated_at       timestamp with time zone NOT NULL DEFAULT now(),
    last_login_at    timestamp with time zone,
    last_login_ip    inet,
    -- Per-credential lockout, carried over from admin_credentials. Counted on
    -- the credential and not on (username, ip) like auth_lockouts: there are a
    -- handful of these rows, and an attacker rotating IPs must not reset it.
    failed_count     integer NOT NULL DEFAULT 0,
    locked_until     timestamp with time zone
);

-- Case-insensitive uniqueness. A login differing only in case is the same
-- login to the person typing it, and two rows that look identical in the panel
-- is how the wrong one gets disabled.
CREATE UNIQUE INDEX IF NOT EXISTS admins_login_key ON admins (lower(login));

-- ---------------------------------------------------------------------------
-- The sessions.
-- ---------------------------------------------------------------------------

-- Admin sessions used to live in user_sessions, told apart by a platform
-- string starting with 'admin-'. A prefix on a free-text column is not an
-- access boundary: every query that walks user_sessions — session listing,
-- bulk revocation, device management, cleanup jobs — had to remember to
-- exclude them, and the one that forgets does something surprising to a
-- credential it does not understand.
--
-- Shape mirrors user_sessions deliberately, because the refresh protocol is
-- the same one: the refresh cookie carries the session id, and the gate is an
-- Ed25519 signature over a server nonce made by the device key pinned here.
CREATE TABLE IF NOT EXISTS admin_sessions (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    admin_id         uuid NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    public_key       bytea NOT NULL,
    fingerprint_hash bytea NOT NULL,
    platform         text NOT NULL DEFAULT 'unknown',
    last_ip          inet,
    last_user_agent  text NOT NULL DEFAULT '',
    created_at       timestamp with time zone NOT NULL DEFAULT now(),
    last_used_at     timestamp with time zone NOT NULL DEFAULT now(),
    expires_at       timestamp with time zone NOT NULL,
    revoked_at       timestamp with time zone,
    revoked_reason   text
);

-- The two reads that matter: "this admin's live sessions" (the panel's own
-- session list, and revoke-all on password change) and the expiry sweep.
CREATE INDEX IF NOT EXISTS admin_sessions_admin_idx
    ON admin_sessions (admin_id) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS admin_sessions_expires_idx
    ON admin_sessions (expires_at) WHERE revoked_at IS NULL;

-- ---------------------------------------------------------------------------
-- Attribution: rows that named an admin by user id now name an admin.
-- ---------------------------------------------------------------------------
--
-- All three columns are NULL in every row at the time of writing, so this is a
-- structural change and not a data migration. ON DELETE SET NULL rather than
-- CASCADE: losing the record of an action because its actor's row went away is
-- exactly backwards for an audit trail.

ALTER TABLE admin_audit_log DROP CONSTRAINT IF EXISTS admin_audit_log_admin_id_fkey;
ALTER TABLE admin_audit_log
    ADD CONSTRAINT admin_audit_log_admin_id_fkey
    FOREIGN KEY (admin_id) REFERENCES admins(id) ON DELETE SET NULL;

ALTER TABLE email_send_log DROP CONSTRAINT IF EXISTS email_send_log_sent_by_admin_id_fkey;
ALTER TABLE email_send_log
    ADD CONSTRAINT email_send_log_sent_by_admin_id_fkey
    FOREIGN KEY (sent_by_admin_id) REFERENCES admins(id) ON DELETE SET NULL;

ALTER TABLE mailer_settings DROP CONSTRAINT IF EXISTS mailer_settings_updated_by_fkey;
ALTER TABLE mailer_settings
    ADD CONSTRAINT mailer_settings_updated_by_fkey
    FOREIGN KEY (updated_by) REFERENCES admins(id) ON DELETE SET NULL;

-- Report review is staff work and only staff work: a report is raised by a user
-- and resolved from the panel, never by another member. So reviewer_id is
-- repointed outright rather than paired with a second column the way the
-- server-level moderation tables below are.

ALTER TABLE user_reports DROP CONSTRAINT IF EXISTS user_reports_reviewer_id_fkey;
ALTER TABLE user_reports
    ADD CONSTRAINT user_reports_reviewer_id_fkey
    FOREIGN KEY (reviewer_id) REFERENCES admins(id) ON DELETE SET NULL;

ALTER TABLE stream_reports DROP CONSTRAINT IF EXISTS stream_reports_reviewer_id_fkey;
ALTER TABLE stream_reports
    ADD CONSTRAINT stream_reports_reviewer_id_fkey
    FOREIGN KEY (reviewer_id) REFERENCES admins(id) ON DELETE SET NULL;

-- notifications.actor_id is deliberately left pointing at users and is NEVER
-- given an administrator. When staff resolve a report the reporter is told the
-- outcome with no actor at all (NULL, the "system originated" shape Insert
-- already writes for an empty actor): which member of staff handled a report
-- is not the reporter's to know, and making it unrepresentable is a better
-- guarantee than a policy of not filling it in.

-- ---------------------------------------------------------------------------
-- Moderation: two kinds of actor, stated as two columns.
-- ---------------------------------------------------------------------------
--
-- A mute or a ban inside a server can be issued by that server's own
-- moderator — an ordinary user — or by staff from the panel. Those are
-- different facts and they point at different tables, so they get different
-- columns and a constraint saying at most one of them is set. Collapsing them
-- into one uuid would mean an id whose meaning depends on a table it does not
-- name, which is how "who muted me" turns into a lookup that can answer wrong.
--
-- At most one, not exactly one: both NULL already means "the system did it"
-- (an expiry sweep, an automated rule), and that stays legal.

ALTER TABLE server_mutes ADD COLUMN IF NOT EXISTS muted_by_admin uuid
    REFERENCES admins(id) ON DELETE SET NULL;
ALTER TABLE server_mutes DROP CONSTRAINT IF EXISTS server_mutes_actor_chk;
ALTER TABLE server_mutes ADD CONSTRAINT server_mutes_actor_chk
    CHECK (num_nonnulls(muted_by, muted_by_admin) <= 1);

ALTER TABLE server_bans ADD COLUMN IF NOT EXISTS banned_by_admin uuid
    REFERENCES admins(id) ON DELETE SET NULL;
ALTER TABLE server_bans DROP CONSTRAINT IF EXISTS server_bans_actor_chk;
ALTER TABLE server_bans ADD CONSTRAINT server_bans_actor_chk
    CHECK (num_nonnulls(banned_by, banned_by_admin) <= 1);

-- ---------------------------------------------------------------------------
-- The old shape goes away.
-- ---------------------------------------------------------------------------
--
-- No compatibility window and no shim: there are no admin rows anywhere, no
-- deployed panel talking to a running server, and a table that still accepts
-- logins is not a harmless leftover.
DROP TABLE IF EXISTS admin_credentials;
