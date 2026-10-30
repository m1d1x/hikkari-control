package auth

// The administrator store.
//
// Replaces admin_credentials (migration 007), which hung off users.id and made
// "is an administrator" a property of a chat account. An `admins` row has its
// own primary key and no foreign key into users: the two populations are
// separate, and nothing about one can be reached by compromising the other.
//
// Lockout policy is intentionally simpler than internal/auth/lockout.go (which
// protects the main login):
//   - Threshold: AdminLockoutThreshold (5) failed attempts.
//   - Backoff: fixed AdminLockoutDuration (15 min). No exponential ramp.
//
// By design: there is a handful of these accounts and each is worth far more
// than a user account, so a flat "wait 15 minutes" is a fair trade for a
// tighter brute-force shield. The counter lives on the credential and not on
// (username, ip) like auth_lockouts, so rotating source addresses does not
// reset it.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/db"
)

// AdminLockoutThreshold is the number of consecutive failed admin login
// attempts that triggers a lockout. Reset to 0 on each successful login.
const AdminLockoutThreshold = 5

// AdminLockoutDuration is the fixed wait imposed once threshold is hit.
const AdminLockoutDuration = 15 * time.Minute

// AdminPasswordMinLen is the shortest password an operator may set. Enforced
// at both doors that set one: the provisioning CLI and the panel's own
// change-password form.
const AdminPasswordMinLen = 12

// ErrAdminNotFound is returned when no admins row matches. The login handler
// maps this to the same generic 401 it returns for a wrong password, so
// callers must not surface "not found" on its own.
var ErrAdminNotFound = errors.New("admin not found")

// AdminRole is what an operator may do (migration 022, task 77.4).
type AdminRole string

const (
	// AdminRoleOwner may do everything, including erasing accounts, banning
	// and changing the platform's configuration.
	AdminRoleOwner AdminRole = "owner"
	// AdminRoleSupport reads, answers tickets and works reports. It is the
	// default for a new operator: the role that can do less is the one you get
	// by forgetting to choose.
	AdminRoleSupport AdminRole = "support"
)

// ParseAdminRole accepts exactly the two roles the schema allows.
func ParseAdminRole(s string) (AdminRole, error) {
	switch AdminRole(s) {
	case AdminRoleOwner, AdminRoleSupport:
		return AdminRole(s), nil
	}
	return "", fmt.Errorf("unknown admin role %q (want owner or support)", s)
}

// Admin is the in-memory view of one `admins` row.
type Admin struct {
	ID           string
	Login        string
	DisplayName  string
	PasswordHash string
	IsDisabled   bool
	Role         AdminRole
	LockedUntil  *time.Time
	FailedCount  int
}

// GetAdminByLogin looks an administrator up by login, case-insensitively —
// matching the unique index, so two logins differing only in case cannot both
// exist and cannot be confused for each other.
func GetAdminByLogin(ctx context.Context, pool db.Pool, login string) (*Admin, error) {
	a := &Admin{}
	err := pool.QueryRow(ctx, `
		SELECT id::text, login, display_name, password_hash, is_disabled,
		       role, locked_until, failed_count
		FROM admins
		WHERE lower(login) = lower($1)
	`, login).Scan(&a.ID, &a.Login, &a.DisplayName, &a.PasswordHash, &a.IsDisabled,
		&a.Role, &a.LockedUntil, &a.FailedCount)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAdminNotFound
		}
		return nil, fmt.Errorf("get admin by login: %w", err)
	}
	return a, nil
}

// GetAdminByID loads an administrator by primary key. Called on every
// authenticated request (the gate re-reads rather than trusting the token's
// claims), so it stays a single indexed lookup.
func GetAdminByID(ctx context.Context, pool db.Pool, id string) (*Admin, error) {
	a := &Admin{}
	err := pool.QueryRow(ctx, `
		SELECT id::text, login, display_name, password_hash, is_disabled,
		       role, locked_until, failed_count
		FROM admins
		WHERE id = $1
	`, id).Scan(&a.ID, &a.Login, &a.DisplayName, &a.PasswordHash, &a.IsDisabled,
		&a.Role, &a.LockedUntil, &a.FailedCount)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAdminNotFound
		}
		return nil, fmt.Errorf("get admin by id: %w", err)
	}
	return a, nil
}

// CreateAdmin inserts an administrator and returns its id. Used by the
// provisioning CLI (`cmd/server admin-create`); there is no self-service
// signup and there is not meant to be one.
func CreateAdmin(ctx context.Context, pool db.Pool, login, displayName, passwordHash string, role AdminRole) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO admins (login, display_name, password_hash, role)
		VALUES ($1, $2, $3, $4)
		RETURNING id::text
	`, login, displayName, passwordHash, string(role)).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create admin: %w", err)
	}
	return id, nil
}

// SetAdminPassword rotates the password and clears any pending lockout — the
// operator plainly has a working channel, since they could run the CLI.
//
// It does NOT revoke live sessions; that is AdminRevokeAllSessions, which the
// caller invokes explicitly so "rotate the password" and "kick everyone out"
// stay separate decisions.
func SetAdminPassword(ctx context.Context, pool db.Pool, login, passwordHash string) error {
	tag, err := pool.Exec(ctx, `
		UPDATE admins
		SET password_hash = $2,
		    failed_count  = 0,
		    locked_until  = NULL,
		    updated_at    = now()
		WHERE lower(login) = lower($1)
	`, login, passwordHash)
	if err != nil {
		return fmt.Errorf("set admin password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAdminNotFound
	}
	return nil
}

// MarkAdminLoginSuccess clears the failure counter and records the successful
// login metadata. Best-effort by the caller: if it errors, the login itself
// still succeeds.
func MarkAdminLoginSuccess(ctx context.Context, pool db.Pool, adminID, ip string) error {
	_, err := pool.Exec(ctx, `
		UPDATE admins
		SET failed_count  = 0,
		    locked_until  = NULL,
		    last_login_at = now(),
		    last_login_ip = $2,
		    updated_at    = now()
		WHERE id = $1
	`, adminID, normalizeIP(ip))
	if err != nil {
		return fmt.Errorf("mark admin login success: %w", err)
	}
	return nil
}

// IncrementAdminFailedLogin bumps failed_count by 1 and, once the new count
// reaches AdminLockoutThreshold, sets locked_until. Returns the new (locked,
// until) state so the caller can send Retry-After without a follow-up read.
func IncrementAdminFailedLogin(ctx context.Context, pool db.Pool, adminID string) (locked bool, until time.Time, err error) {
	var lockedUntil *time.Time
	err = pool.QueryRow(ctx, `
		UPDATE admins
		SET failed_count = failed_count + 1,
		    locked_until = CASE
		        WHEN failed_count + 1 >= $2 THEN now() + ($3 || ' seconds')::interval
		        ELSE locked_until
		    END,
		    updated_at = now()
		WHERE id = $1
		RETURNING locked_until
	`, adminID, AdminLockoutThreshold, fmt.Sprintf("%d", int(AdminLockoutDuration.Seconds()))).Scan(&lockedUntil)
	if err != nil {
		return false, time.Time{}, fmt.Errorf("increment admin failed login: %w", err)
	}
	if lockedUntil != nil && lockedUntil.After(time.Now()) {
		return true, *lockedUntil, nil
	}
	return false, time.Time{}, nil
}

// IsAdminLocked reports whether the account is currently in lockout, and until
// when. Read separately from the row the login flow already holds so the
// freshest value wins.
func IsAdminLocked(ctx context.Context, pool db.Pool, adminID string) (bool, time.Time, error) {
	var lockedUntil *time.Time
	err := pool.QueryRow(ctx, `SELECT locked_until FROM admins WHERE id = $1`, adminID).Scan(&lockedUntil)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, time.Time{}, ErrAdminNotFound
		}
		return false, time.Time{}, fmt.Errorf("check admin lock: %w", err)
	}
	if lockedUntil != nil && lockedUntil.After(time.Now()) {
		return true, *lockedUntil, nil
	}
	return false, time.Time{}, nil
}
