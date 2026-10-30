package auth

// Session store for the admin plane.
//
// Admin sessions used to be rows in user_sessions whose `platform` column
// started with "admin-". A prefix on a free-text column is not a boundary:
// every query that walks user_sessions — the device list, bulk revocation,
// cleanup sweeps — had to remember to exclude them, and the one that forgot
// did something surprising to a credential it did not understand. They live in
// their own table now (migration 007).
//
// Protocol is the one the panel already speaks: the refresh cookie carries the
// session id, and the gate is an Ed25519 signature over a server-issued nonce,
// made by the device key pinned at login. The id is not a secret and is not
// treated as one.
//
// Unlike the app plane there is no in-memory revocation cache. The gate reads
// the session row on every request instead: admin traffic is a handful of
// requests per second at most, and a synchronous read is strictly stronger
// than a cache that fails open (revocation.go) — a revoked administrator stops
// at the next request rather than at the next poll.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/db"
)

// AdminSessionTTL is how long an admin session may go unused before it has to
// be re-established with a password. Far shorter than the app's RefreshTTL
// (60 days): a panel session is the most valuable credential in the system,
// and an operator signing in again once a week is not a hardship.
const AdminSessionTTL = 7 * 24 * time.Hour

// ErrAdminSessionNotFound covers "no such row", "revoked" and "expired"
// alike. The caller turns all three into the same 401: which of them it was
// is useful in a log, never in a response.
var ErrAdminSessionNotFound = errors.New("admin session not found")

// AdminSession is a live row of admin_sessions.
type AdminSession struct {
	ID        string
	AdminID   string
	PublicKey []byte
	ExpiresAt time.Time
}

// CreateAdminSessionParams is what a successful login pins to the session.
type CreateAdminSessionParams struct {
	AdminID         string
	PublicKey       []byte
	FingerprintHash []byte
	Platform        string
	IP              string
	UserAgent       string
}

// CreateAdminSession records a new session and returns its id.
func CreateAdminSession(ctx context.Context, pool db.Pool, p CreateAdminSessionParams) (string, error) {
	if p.AdminID == "" {
		return "", errors.New("admin session: empty admin id")
	}
	if len(p.PublicKey) == 0 {
		return "", errors.New("admin session: empty device public key")
	}
	if len(p.FingerprintHash) != FingerprintHashSize {
		return "", fmt.Errorf("admin session: fingerprint hash is %d bytes, want %d",
			len(p.FingerprintHash), FingerprintHashSize)
	}

	now := time.Now()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO admin_sessions
			(admin_id, public_key, fingerprint_hash, platform,
			 last_ip, last_user_agent, created_at, last_used_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $8)
		RETURNING id::text
	`,
		p.AdminID, p.PublicKey, p.FingerprintHash, platformOrDefault(p.Platform),
		inetOrNil(p.IP), p.UserAgent, now, now.Add(AdminSessionTTL),
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert admin session: %w", err)
	}
	return id, nil
}

// LookupAdminSession returns the session only when it is live — not revoked
// and not expired. The three failure modes collapse into
// ErrAdminSessionNotFound on purpose.
func LookupAdminSession(ctx context.Context, pool db.Pool, sessionID string) (*AdminSession, error) {
	if sessionID == "" {
		return nil, ErrAdminSessionNotFound
	}
	s := &AdminSession{}
	err := pool.QueryRow(ctx, `
		SELECT id::text, admin_id::text, public_key, expires_at
		FROM admin_sessions
		WHERE id = $1 AND revoked_at IS NULL AND expires_at > now()
	`, sessionID).Scan(&s.ID, &s.AdminID, &s.PublicKey, &s.ExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAdminSessionNotFound
		}
		return nil, fmt.Errorf("lookup admin session: %w", err)
	}
	return s, nil
}

// IsAdminSessionLive is the per-request check the gate makes: one indexed
// read, answering "is this token's session still good".
func IsAdminSessionLive(ctx context.Context, pool db.Pool, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	var live bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM admin_sessions
			WHERE id = $1 AND revoked_at IS NULL AND expires_at > now()
		)
	`, sessionID).Scan(&live)
	return err == nil && live
}

// TouchAdminSession slides the session's expiry forward and records where it
// was last used from. Returns ErrAdminSessionNotFound when the row is no
// longer live, so a refresh against a revoked session fails rather than
// resurrecting it.
func TouchAdminSession(ctx context.Context, pool db.Pool, sessionID, ip, userAgent string) error {
	now := time.Now()
	tag, err := pool.Exec(ctx, `
		UPDATE admin_sessions
		SET last_used_at    = $1,
		    expires_at      = $2,
		    last_ip         = $3,
		    last_user_agent = $4
		WHERE id = $5 AND revoked_at IS NULL AND expires_at > $1
	`, now, now.Add(AdminSessionTTL), inetOrNil(ip), userAgent, sessionID)
	if err != nil {
		return fmt.Errorf("touch admin session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAdminSessionNotFound
	}
	return nil
}

// RevokeAdminSession ends one session. Idempotent.
func RevokeAdminSession(ctx context.Context, pool db.Pool, sessionID, reason string) error {
	_, err := pool.Exec(ctx, `
		UPDATE admin_sessions
		SET revoked_at = now(), revoked_reason = $1
		WHERE id = $2 AND revoked_at IS NULL
	`, reason, sessionID)
	if err != nil {
		return fmt.Errorf("revoke admin session: %w", err)
	}
	return nil
}

// RevokeAllAdminSessions ends every live session for one administrator.
// Called when the account is disabled and available to the provisioning CLI
// after a password rotation.
func RevokeAllAdminSessions(ctx context.Context, pool db.Pool, adminID, reason string) (int64, error) {
	tag, err := pool.Exec(ctx, `
		UPDATE admin_sessions
		SET revoked_at = now(), revoked_reason = $1
		WHERE admin_id = $2 AND revoked_at IS NULL
	`, reason, adminID)
	if err != nil {
		return 0, fmt.Errorf("revoke all admin sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

// RevokeOtherAdminSessions ends every live session of one administrator except
// keep. A password changed from the panel signs out every other browser while
// the one that made the change stays in.
func RevokeOtherAdminSessions(ctx context.Context, pool db.Pool, adminID, keep, reason string) (int64, error) {
	tag, err := pool.Exec(ctx, `
		UPDATE admin_sessions
		SET revoked_at = now(), revoked_reason = $1
		WHERE admin_id = $2 AND id <> $3 AND revoked_at IS NULL
	`, reason, adminID, keep)
	if err != nil {
		return 0, fmt.Errorf("revoke other admin sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

// inetOrNil keeps a malformed address out of an inet column, which would
// otherwise fail the whole insert over a value nothing reads for authorisation.
//
// Admin IPs are stored in the clear, unlike user_sessions.last_ip_ct: the
// encryption there protects the product's users, while an operator's access
// log is meant to be readable by whoever audits the panel.
func inetOrNil(ip string) any {
	if ip == "" || net.ParseIP(ip) == nil {
		return nil
	}
	return ip
}

// AdminGateState is what the admin gate needs to know about one request's
// session and operator, read in one query.
type AdminGateState struct {
	Admin          *Admin
	SteppedUpUntil *time.Time
}

// LoadAdminGateState reads the live session and its operator together. A
// revoked or expired session, or a session that belongs to another operator,
// answers ErrAdminSessionNotFound.
func LoadAdminGateState(ctx context.Context, pool db.Pool, sessionID, adminID string) (*AdminGateState, error) {
	if sessionID == "" || adminID == "" {
		return nil, ErrAdminSessionNotFound
	}
	a := &Admin{}
	st := &AdminGateState{Admin: a}
	err := pool.QueryRow(ctx, `
		SELECT a.id::text, a.login, a.display_name, a.is_disabled, a.role, s.stepped_up_until
		FROM admin_sessions s
		JOIN admins a ON a.id = s.admin_id
		WHERE s.id = $1 AND s.admin_id = $2
		  AND s.revoked_at IS NULL AND s.expires_at > now()
	`, sessionID, adminID).Scan(&a.ID, &a.Login, &a.DisplayName, &a.IsDisabled, &a.Role, &st.SteppedUpUntil)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAdminSessionNotFound
		}
		return nil, fmt.Errorf("load admin gate state: %w", err)
	}
	return st, nil
}
