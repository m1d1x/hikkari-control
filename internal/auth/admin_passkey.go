package auth

// The admin plane's second factor: passkeys, recovery codes and the console
// enrolment code (roadmap task 77.2, migration 022).
//
// # The shape, and why each piece is there
//
//   - A passkey (WebAuthn) is the factor. It lives in the operator's iCloud
//     Keychain or password manager, so it is not tied to one phone, and it is
//     bound to the panel's origin by the browser, so a look-alike page cannot
//     collect it. The relying party is `localhost` by default: the panel is
//     only ever opened through `ssh -L` (task 77.1), and the address the
//     browser sees through a tunnel never changes.
//   - Ten recovery codes are the way back when every device holding a passkey
//     is gone. Shown once, stored as hashes, each usable once.
//   - The FIRST passkey can only be registered with a code printed by the CLI
//     on the box (`admin-create`, `admin-2fa-reset`). A password alone — the
//     thing that leaks — therefore cannot enrol an attacker's own key; it takes
//     shell access, which takes the operator's SSH key.
//
// Ceremonies (a challenge issued, an answer pending) live in Postgres rather
// than process memory: a restart between the two halves of a login should cost
// one retry, and a ceremony read is a single indexed DELETE … RETURNING.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/db"
)

const (
	// adminCeremonyTTL bounds how long a challenge stays answerable.
	adminCeremonyTTL = 5 * time.Minute
	// AdminStepUpWindow is how long one touch authorizes destructive actions.
	// Long enough to work a short queue without re-prompting on every click,
	// short enough that a session left open is not a standing authorization.
	AdminStepUpWindow = 5 * time.Minute
	// AdminEnrollCodeTTL is how long a CLI-issued enrolment code stays valid.
	AdminEnrollCodeTTL = 24 * time.Hour
	// adminRecoveryCodeCount is how many recovery codes one issue produces.
	adminRecoveryCodeCount = 10

	defaultAdminRPID   = "localhost"
	defaultAdminOrigin = "http://localhost:5174"
)

// Ceremony purposes, as the admin_ceremonies CHECK constraint spells them.
const (
	ceremonyLogin  = "login"
	ceremonyEnroll = "enroll"
	ceremonyStepUp = "step_up"
	ceremonyAddKey = "add_key"
)

var (
	// ErrAdminCeremonyGone means the ceremony id is unknown, expired, already
	// used, or belongs to another session. One error for all four: telling
	// them apart helps only someone guessing ids.
	ErrAdminCeremonyGone = errors.New("admin ceremony expired or unknown")
	// ErrAdminCodeInvalid is a wrong recovery or enrolment code.
	ErrAdminCodeInvalid = errors.New("admin code invalid")
	// ErrAdminLastPasskey refuses removing the only passkey: an operator left
	// with none could not sign in again without the CLI.
	ErrAdminLastPasskey = errors.New("cannot remove the last passkey")
)

// AdminWebAuthn is the relying party the admin plane verifies passkeys with.
type AdminWebAuthn struct {
	wa *webauthn.WebAuthn
}

// NewAdminWebAuthn builds the relying party for one RP ID and the panel
// origins allowed to run ceremonies against it.
func NewAdminWebAuthn(rpID string, origins []string) (*AdminWebAuthn, error) {
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: "nekko admin",
		RPOrigins:     origins,
		// Platform authenticators and security keys alike, but the person must
		// be verified (Touch ID, PIN) — a tap on a key someone else is holding
		// is not a second factor of the operator.
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
			UserVerification: protocol.VerificationRequired,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("admin webauthn config: %w", err)
	}
	return &AdminWebAuthn{wa: wa}, nil
}

// AdminWebAuthnFromEnv reads ADMIN_WEBAUTHN_RP_ID and ADMIN_WEBAUTHN_ORIGINS
// (comma-separated). Both default to the tunnelled panel: localhost and
// http://localhost:5174.
func AdminWebAuthnFromEnv() (*AdminWebAuthn, error) {
	rpID := strings.TrimSpace(os.Getenv("ADMIN_WEBAUTHN_RP_ID"))
	if rpID == "" {
		rpID = defaultAdminRPID
	}
	var origins []string
	for _, o := range strings.Split(os.Getenv("ADMIN_WEBAUTHN_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			origins = append(origins, o)
		}
	}
	if len(origins) == 0 {
		origins = []string{defaultAdminOrigin}
	}
	return NewAdminWebAuthn(rpID, origins)
}

// adminWAUser adapts an operator to the library's User. The user handle is the
// admin's UUID: random, stable, and meaningless outside this table.
type adminWAUser struct {
	id    uuid.UUID
	login string
	name  string
	creds []webauthn.Credential
}

func (u *adminWAUser) WebAuthnID() []byte { return u.id[:] }
func (u *adminWAUser) WebAuthnName() string {
	return u.login
}
func (u *adminWAUser) WebAuthnDisplayName() string {
	if u.name != "" {
		return u.name
	}
	return u.login
}
func (u *adminWAUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func loadAdminWAUser(ctx context.Context, q db.DBTX, a *Admin) (*adminWAUser, error) {
	id, err := uuid.Parse(a.ID)
	if err != nil {
		return nil, fmt.Errorf("admin id: %w", err)
	}
	creds, err := loadAdminCredentials(ctx, q, a.ID)
	if err != nil {
		return nil, err
	}
	return &adminWAUser{id: id, login: a.Login, name: a.DisplayName, creds: creds}, nil
}

// ── passkey store ─────────────────────────────────────────────────────────────

// AdminPasskeyView is a passkey as the panel lists it. No key material.
type AdminPasskeyView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

func loadAdminCredentials(ctx context.Context, q db.DBTX, adminID string) ([]webauthn.Credential, error) {
	rows, err := q.Query(ctx, `SELECT credential FROM admin_passkeys WHERE admin_id = $1 ORDER BY created_at`, adminID)
	if err != nil {
		return nil, fmt.Errorf("load admin passkeys: %w", err)
	}
	defer rows.Close()
	var out []webauthn.Credential
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan admin passkey: %w", err)
		}
		var c webauthn.Credential
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("decode admin passkey: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListAdminPasskeys returns an operator's passkeys for the security page.
func ListAdminPasskeys(ctx context.Context, q db.DBTX, adminID string) ([]AdminPasskeyView, error) {
	rows, err := q.Query(ctx, `
		SELECT id::text, name, created_at, last_used_at
		FROM admin_passkeys WHERE admin_id = $1 ORDER BY created_at`, adminID)
	if err != nil {
		return nil, fmt.Errorf("list admin passkeys: %w", err)
	}
	defer rows.Close()
	out := []AdminPasskeyView{}
	for rows.Next() {
		var v AdminPasskeyView
		if err := rows.Scan(&v.ID, &v.Name, &v.CreatedAt, &v.LastUsedAt); err != nil {
			return nil, fmt.Errorf("scan admin passkey: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CountAdminPasskeys reports how many passkeys an operator has.
func CountAdminPasskeys(ctx context.Context, q db.DBTX, adminID string) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM admin_passkeys WHERE admin_id = $1`, adminID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count admin passkeys: %w", err)
	}
	return n, nil
}

func insertAdminPasskey(ctx context.Context, q db.DBTX, adminID, name string, c *webauthn.Credential) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode admin passkey: %w", err)
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO admin_passkeys (admin_id, credential_id, credential, name)
		VALUES ($1, $2, $3::jsonb, $4)`,
		adminID, c.ID, string(raw), strings.TrimSpace(name)); err != nil {
		return fmt.Errorf("insert admin passkey: %w", err)
	}
	return nil
}

// recordAdminPasskeyUse writes back what a login changes on the credential —
// the sign counter and the backup flags — so the next verification compares
// against the latest values rather than the ones from enrolment.
func recordAdminPasskeyUse(ctx context.Context, q db.DBTX, adminID string, c *webauthn.Credential) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode admin passkey: %w", err)
	}
	if _, err := q.Exec(ctx, `
		UPDATE admin_passkeys SET credential = $3::jsonb, last_used_at = now()
		WHERE admin_id = $1 AND credential_id = $2`,
		adminID, c.ID, string(raw)); err != nil {
		return fmt.Errorf("update admin passkey: %w", err)
	}
	return nil
}

// DeleteAdminPasskey removes one passkey, refusing the last one.
func DeleteAdminPasskey(ctx context.Context, pool db.Pool, adminID, passkeyID string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	// Lock the operator's row so two concurrent removals cannot each see
	// «there is another one» and leave zero.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM admins WHERE id = $1 FOR UPDATE`, adminID); err != nil {
		return fmt.Errorf("lock admin: %w", err)
	}
	n, err := CountAdminPasskeys(ctx, tx, adminID)
	if err != nil {
		return err
	}
	if n <= 1 {
		return ErrAdminLastPasskey
	}
	tag, err := tx.Exec(ctx, `DELETE FROM admin_passkeys WHERE id = $1 AND admin_id = $2`, passkeyID, adminID)
	if err != nil {
		return fmt.Errorf("delete admin passkey: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAdminCeremonyGone
	}
	return tx.Commit(ctx)
}

// ── ceremonies ────────────────────────────────────────────────────────────────

// adminCeremonyData is what a ceremony row remembers between its two halves.
type adminCeremonyData struct {
	Session *webauthn.SessionData `json:"session,omitempty"`
	// Device is the binding the session will be minted with (login, enroll).
	// Captured at the password step and never re-read from the browser.
	DevicePublicKey   []byte `json:"devicePublicKey,omitempty"`
	DeviceFingerprint []byte `json:"deviceFingerprint,omitempty"`
	DevicePlatform    string `json:"devicePlatform,omitempty"`
}

func createAdminCeremony(ctx context.Context, q db.DBTX, adminID, purpose, sessionID string, d adminCeremonyData) (string, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return "", fmt.Errorf("encode ceremony: %w", err)
	}
	var sid any
	if sessionID != "" {
		sid = sessionID
	}
	var id string
	if err := q.QueryRow(ctx, `
		INSERT INTO admin_ceremonies (admin_id, purpose, session_id, data, expires_at)
		VALUES ($1, $2, $3, $4::jsonb, now() + make_interval(secs => $5))
		RETURNING id::text`,
		adminID, purpose, sid, string(raw), adminCeremonyTTL.Seconds()).Scan(&id); err != nil {
		return "", fmt.Errorf("insert ceremony: %w", err)
	}
	return id, nil
}

// takeAdminCeremony consumes a ceremony: it can be answered once. sessionID
// must match for session-bound purposes and be empty for the pre-session ones.
func takeAdminCeremony(ctx context.Context, q db.DBTX, id, purpose, sessionID string) (string, adminCeremonyData, error) {
	if _, err := uuid.Parse(id); err != nil {
		return "", adminCeremonyData{}, ErrAdminCeremonyGone
	}
	var (
		adminID string
		raw     []byte
	)
	err := q.QueryRow(ctx, `
		DELETE FROM admin_ceremonies
		WHERE id = $1 AND purpose = $2
		  AND session_id IS NOT DISTINCT FROM $3::uuid
		  AND expires_at > now()
		RETURNING admin_id::text, data`,
		id, purpose, nullIfEmpty(sessionID)).Scan(&adminID, &raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", adminCeremonyData{}, ErrAdminCeremonyGone
		}
		return "", adminCeremonyData{}, fmt.Errorf("take ceremony: %w", err)
	}
	var d adminCeremonyData
	if err := json.Unmarshal(raw, &d); err != nil {
		return "", adminCeremonyData{}, fmt.Errorf("decode ceremony: %w", err)
	}
	return adminID, d, nil
}

// peekAdminCeremony reads a ceremony without consuming it. Used where the
// answer is a typed code: a typo must cost one attempt against the lockout,
// not the whole sign-in — making the operator retype the password over a
// mistyped code is how people start pasting codes from insecure places.
func peekAdminCeremony(ctx context.Context, q db.DBTX, id, purpose string) (string, error) {
	if _, err := uuid.Parse(id); err != nil {
		return "", ErrAdminCeremonyGone
	}
	var adminID string
	err := q.QueryRow(ctx, `
		SELECT admin_id::text FROM admin_ceremonies
		WHERE id = $1 AND purpose = $2 AND session_id IS NULL AND expires_at > now()`,
		id, purpose).Scan(&adminID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrAdminCeremonyGone
		}
		return "", fmt.Errorf("peek ceremony: %w", err)
	}
	return adminID, nil
}

// PurgeExpiredAdminCeremonies drops ceremonies nobody answered.
func PurgeExpiredAdminCeremonies(ctx context.Context, pool db.Pool) error {
	_, err := pool.Exec(ctx, `DELETE FROM admin_ceremonies WHERE expires_at <= now()`)
	return err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ── one-time codes ───────────────────────────────────────────────────────────

// Crockford base32 without I, L, O, U: nothing a person can misread when
// copying a code off paper or a terminal.
const adminCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newAdminCode returns a code like 7K3F-Q9WD-M2XA: twelve symbols, 60 bits.
func newAdminCode() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}
	var sb strings.Builder
	for i, v := range b {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(adminCodeAlphabet[int(v)%len(adminCodeAlphabet)])
	}
	return sb.String(), nil
}

// normalizeAdminCode accepts what a person types: any case, with or without
// dashes and spaces, and the usual look-alikes for 0 and 1.
func normalizeAdminCode(s string) string {
	s = strings.ToUpper(s)
	var sb strings.Builder
	for _, r := range s {
		switch r {
		case '-', ' ', '\t':
			continue
		case 'O':
			r = '0'
		case 'I', 'L':
			r = '1'
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

func hashAdminCode(code string) []byte {
	sum := sha256.Sum256([]byte("nekko-admin-code-v1:" + normalizeAdminCode(code)))
	return sum[:]
}

// IssueAdminEnrollCode wipes the operator's passkeys, recovery codes and live
// sessions and returns a fresh enrolment code. The CLI's reset: whoever runs
// it is on the box, which is the trust root of the whole plane.
func IssueAdminEnrollCode(ctx context.Context, pool db.Pool, adminID string) (string, error) {
	code, err := newAdminCode()
	if err != nil {
		return "", err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	for _, q := range []string{
		`DELETE FROM admin_passkeys WHERE admin_id = $1`,
		`DELETE FROM admin_recovery_codes WHERE admin_id = $1`,
		`DELETE FROM admin_ceremonies WHERE admin_id = $1`,
		`UPDATE admin_sessions SET revoked_at = now(), revoked_reason = 'second_factor_reset'
		 WHERE admin_id = $1 AND revoked_at IS NULL`,
	} {
		if _, err := tx.Exec(ctx, q, adminID); err != nil {
			return "", fmt.Errorf("reset second factor: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE admins
		SET enroll_code_hash = $2,
		    enroll_code_expires_at = now() + make_interval(secs => $3),
		    failed_count = 0, locked_until = NULL, updated_at = now()
		WHERE id = $1`,
		adminID, hashAdminCode(code), AdminEnrollCodeTTL.Seconds()); err != nil {
		return "", fmt.Errorf("store enrol code: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return code, nil
}

// adminEnrollCodeMatches checks a typed enrolment code without consuming it.
func adminEnrollCodeMatches(ctx context.Context, q db.DBTX, adminID, code string) (bool, error) {
	var (
		hash    []byte
		expires *time.Time
	)
	if err := q.QueryRow(ctx, `SELECT enroll_code_hash, enroll_code_expires_at FROM admins WHERE id = $1`,
		adminID).Scan(&hash, &expires); err != nil {
		return false, fmt.Errorf("read enrol code: %w", err)
	}
	if hash == nil || expires == nil || !expires.After(time.Now()) {
		return false, nil
	}
	return subtle.ConstantTimeCompare(hash, hashAdminCode(code)) == 1, nil
}

// adminHasPendingEnrollment reports whether a live enrolment code exists.
func adminHasPendingEnrollment(ctx context.Context, q db.DBTX, adminID string) (bool, error) {
	var pending bool
	if err := q.QueryRow(ctx, `
		SELECT enroll_code_hash IS NOT NULL AND enroll_code_expires_at > now()
		FROM admins WHERE id = $1`, adminID).Scan(&pending); err != nil {
		return false, fmt.Errorf("read enrol state: %w", err)
	}
	return pending, nil
}

// issueAdminRecoveryCodes replaces every recovery code with ten new ones and
// returns them in clear — the only time they exist outside the operator's
// hands.
func issueAdminRecoveryCodes(ctx context.Context, q db.DBTX, adminID string) ([]string, error) {
	if _, err := q.Exec(ctx, `DELETE FROM admin_recovery_codes WHERE admin_id = $1`, adminID); err != nil {
		return nil, fmt.Errorf("drop recovery codes: %w", err)
	}
	codes := make([]string, 0, adminRecoveryCodeCount)
	for len(codes) < adminRecoveryCodeCount {
		c, err := newAdminCode()
		if err != nil {
			return nil, err
		}
		if _, err := q.Exec(ctx, `
			INSERT INTO admin_recovery_codes (admin_id, code_hash) VALUES ($1, $2)
			ON CONFLICT DO NOTHING`, adminID, hashAdminCode(c)); err != nil {
			return nil, fmt.Errorf("insert recovery code: %w", err)
		}
		codes = append(codes, c)
	}
	return codes, nil
}

// consumeAdminRecoveryCode spends one recovery code. The UPDATE is the check:
// a code already used matches no row, so two racing logins cannot both spend
// it.
func consumeAdminRecoveryCode(ctx context.Context, q db.DBTX, adminID, code string) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE admin_recovery_codes SET used_at = now()
		WHERE admin_id = $1 AND code_hash = $2 AND used_at IS NULL`,
		adminID, hashAdminCode(code))
	if err != nil {
		return false, fmt.Errorf("consume recovery code: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// CountAdminRecoveryCodesLeft reports unused recovery codes.
func CountAdminRecoveryCodesLeft(ctx context.Context, q db.DBTX, adminID string) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM admin_recovery_codes WHERE admin_id = $1 AND used_at IS NULL`,
		adminID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count recovery codes: %w", err)
	}
	return n, nil
}

// ── step-up ──────────────────────────────────────────────────────────────────

// markAdminSteppedUp opens the destructive-action window on one session.
func markAdminSteppedUp(ctx context.Context, q db.DBTX, sessionID string) (time.Time, error) {
	var until time.Time
	if err := q.QueryRow(ctx, `
		UPDATE admin_sessions SET stepped_up_until = now() + make_interval(secs => $2)
		WHERE id = $1 AND revoked_at IS NULL
		RETURNING stepped_up_until`, sessionID, AdminStepUpWindow.Seconds()).Scan(&until); err != nil {
		return time.Time{}, fmt.Errorf("mark stepped up: %w", err)
	}
	return until, nil
}
