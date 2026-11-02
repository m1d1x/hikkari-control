package auth

// Tokens for the ops plane: the agent that reads tester logs and asks for
// diagnostics. The admin gate reads only the panel's cookie and says a machine
// caller «gets its own credential class rather than a hole in the human one»;
// this is that class. A token opens /api/v1/ops/* and nothing else, and reaches
// it only through the same SSH tunnel as the panel. It is minted on the box
// (`hub ops-token issue`), so holding one starts from holding the SSH key.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/db"
)

// opsTokenPrefix makes a token recognizable in a config or a leak scan.
const opsTokenPrefix = "hko_"

// ErrOpsTokenInvalid means the token is unknown or revoked.
var ErrOpsTokenInvalid = errors.New("invalid ops token")

func hashOpsToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// IssueOpsToken mints a token. It is returned once; only its hash is kept.
func IssueOpsToken(ctx context.Context, q db.DBTX, label string) (string, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return "", fmt.Errorf("ops token: label is required")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("ops token: %w", err)
	}
	token := opsTokenPrefix + hex.EncodeToString(raw)
	if _, err := q.Exec(ctx, `INSERT INTO admin_ops_tokens (label, token_hash) VALUES ($1, $2)`, label, hashOpsToken(token)); err != nil {
		return "", fmt.Errorf("ops token: %w", err)
	}
	return token, nil
}

// RevokeOpsTokens revokes every live token under label and says how many.
func RevokeOpsTokens(ctx context.Context, q db.DBTX, label string) (int64, error) {
	tag, err := q.Exec(ctx, `UPDATE admin_ops_tokens SET revoked_at = now() WHERE label = $1 AND revoked_at IS NULL`, label)
	if err != nil {
		return 0, fmt.Errorf("ops token: %w", err)
	}
	return tag.RowsAffected(), nil
}

// CheckOpsToken returns the label of a live token and marks it used.
func CheckOpsToken(ctx context.Context, q db.DBTX, token string) (string, error) {
	if !strings.HasPrefix(token, opsTokenPrefix) {
		return "", ErrOpsTokenInvalid
	}
	var label string
	err := q.QueryRow(ctx, `
		UPDATE admin_ops_tokens SET last_used_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL
		RETURNING label
	`, hashOpsToken(token)).Scan(&label)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrOpsTokenInvalid
	}
	if err != nil {
		return "", fmt.Errorf("ops token: %w", err)
	}
	return label, nil
}
