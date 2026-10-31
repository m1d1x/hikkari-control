package subscription

// keys.go is the relay side of a subscription: the access keys an account
// dials with, which relays learn them, and what each key carried.
//
// A key is random and stands for nothing a relay could trace: the relay holds
// the keys of the accounts granted to it and counts bytes per key, the hub
// alone knows which account a key is. So a seized relay gives away a list of
// random strings, and the hub can still cut one subscription off on every
// relay at once (its keys leave the relays' lists on their next pulse) and
// count its traffic across every relay it has moved through.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/entry"
)

// accessKey is one account's credentials on the fleet.
type accessKey struct {
	vlessID string
	hy2Auth string
}

func newAccessKey() (accessKey, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return accessKey{}, err
	}
	return accessKey{vlessID: uuid.NewString(), hy2Auth: hex.EncodeToString(raw)}, nil
}

func insertAccessKey(ctx context.Context, tx pgx.Tx, account uuid.UUID) error {
	k, err := newAccessKey()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO hub_access_keys (account_id, vless_id, hy2_auth) VALUES ($1, $2, $3)`, account, k.vlessID, k.hy2Auth)
	return err
}

func (s *Service) accessKey(ctx context.Context, account uuid.UUID) (accessKey, error) {
	var k accessKey
	err := s.pool.QueryRow(ctx,
		`SELECT vless_id::text, hy2_auth FROM hub_access_keys WHERE account_id = $1`, account).Scan(&k.vlessID, &k.hy2Auth)
	if err != nil {
		return accessKey{}, fmt.Errorf("subscription: access key: %w", err)
	}
	return k, nil
}

// withKey puts the account's own credentials into a relay record in place of
// the relay's shared one.
func withKey(rec entry.RelayRecord, k accessKey) entry.RelayRecord {
	if p := rec.RealityParams; p != nil {
		c := *p
		c.UUID = k.vlessID
		rec.RealityParams = &c
	}
	if p := rec.XhttpParams; p != nil {
		c := *p
		c.UUID = k.vlessID
		rec.XhttpParams = &c
	}
	if p := rec.HysteriaParams; p != nil {
		c := *p
		c.Auth = k.hy2Auth
		rec.HysteriaParams = &c
	}
	return rec
}

// Relay keys: the keys of accounts with a live subscription and a live grant
// on the relay, in the relay protocol's own credential.
const relayKeys = `
	SELECT CASE WHEN $2 = 'hysteria2' THEN k.hy2_auth ELSE k.vless_id::text END
	FROM entry_grants g
	JOIN entry_relays r ON r.id = g.relay_id
	JOIN hub_access_keys k ON k.account_id = g.account_id
	WHERE r.name = $1 AND g.revoked_at IS NULL
	  AND EXISTS (SELECT 1 FROM hub_subscriptions s WHERE s.account_id = g.account_id AND s.revoked_at IS NULL)
	ORDER BY 1`

// KeysOf is the full list of keys relay must let through ([entry.RelayKeys]).
func (s *Service) KeysOf(ctx context.Context, relay, proto string) ([]string, error) {
	rows, err := s.pool.Query(ctx, relayKeys, relay, proto)
	if err != nil {
		return nil, err
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	if keys == nil {
		keys = []string{}
	}
	return keys, nil
}

var bootRe = regexp.MustCompile(`^[0-9a-f]{16,64}$`)

// Totals only ever grow within one run of an edge; GREATEST keeps a pulse that
// arrives late from rolling a row back.
const upsertUsage = `
	INSERT INTO hub_key_usage (account_id, relay, boot, bytes_up, bytes_down)
	SELECT k.account_id, $2, $3, $4, $5 FROM hub_access_keys k
	WHERE k.vless_id::text = $1 OR k.hy2_auth = $1
	ON CONFLICT (account_id, relay, boot) DO UPDATE
	SET bytes_up = GREATEST(hub_key_usage.bytes_up, EXCLUDED.bytes_up),
	    bytes_down = GREATEST(hub_key_usage.bytes_down, EXCLUDED.bytes_down),
	    updated_at = now()`

// RecordUsage files what each key carried through relay during the edge run
// boot ([entry.RelayKeys]). A key the hub does not know is dropped: it counts
// against nobody.
func (s *Service) RecordUsage(ctx context.Context, relay, boot string, usage map[string]entry.KeyUsage) error {
	if len(usage) == 0 {
		return nil
	}
	if !bootRe.MatchString(boot) {
		return fmt.Errorf("subscription: usage boot %q", boot)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	for key, u := range usage {
		if _, err := tx.Exec(ctx, upsertUsage, key, relay, boot, max(u.Up, 0), max(u.Down, 0)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Usage is what name's subscription has carried, over every relay and run.
func (s *Service) Usage(ctx context.Context, name string) (entry.KeyUsage, error) {
	var u entry.KeyUsage
	err := s.pool.QueryRow(ctx, `
		SELECT coalesce(sum(u.bytes_up), 0), coalesce(sum(u.bytes_down), 0)
		FROM hub_key_usage u JOIN hub_accounts a ON a.id = u.account_id
		WHERE a.name = $1`, name).Scan(&u.Up, &u.Down)
	return u, err
}
