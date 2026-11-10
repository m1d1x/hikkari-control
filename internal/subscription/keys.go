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
	// awgPrivate and awgAddress are empty until the account's first AmneziaWG
	// link (awg.go).
	awgPrivate string
	awgAddress string
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
		`SELECT vless_id::text, hy2_auth, coalesce(awg_private, ''), coalesce(host(awg_address), '')
		 FROM hub_access_keys WHERE account_id = $1`, account).Scan(&k.vlessID, &k.hy2Auth, &k.awgPrivate, &k.awgAddress)
	if err != nil {
		return accessKey{}, fmt.Errorf("subscription: access key: %w", err)
	}
	return k, nil
}

// withKey puts the account's own credentials into a relay record in place of
// the relay's shared one.
func withKey(rec entry.RelayRecord, k accessKey) entry.RelayRecord {
	if p := rec.NaiveParams; p != nil {
		c := *p
		c.User, c.Pass = "hikkari", k.hy2Auth
		rec.NaiveParams = &c
	}
	if p := rec.HikkariParams; p != nil {
		c := *p
		c.User, c.Pass = "hikkari", k.hy2Auth
		rec.HikkariParams = &c
	}
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
	if p := rec.AwgParams; p != nil {
		c := *p
		c.ClientPrivateKey, c.ClientAddress = k.awgPrivate, k.awgAddress
		rec.AwgParams = &c
	}
	return rec
}

// Relay keys: the keys of accounts with a live subscription (plans.go) and a
// live grant on the relay, in the relay protocol's own credential.
//
// A grant the current drain revoked counts as live while the relay is still
// out of the pool for the rollout: its holder was told to move, but a client
// moves only on its next subscription fetch, and a key gone from the relay
// before that turns the profile it still dials into «invalid request user id»
// (incident 09.11). The restart of the swap cuts the holder off soon enough.
const relayKeys = `
	SELECT DISTINCT CASE $2
		WHEN 'hysteria2' THEN k.hy2_auth
 WHEN 'naive' THEN k.hy2_auth
 WHEN 'hikkari' THEN k.hy2_auth
		WHEN 'amneziawg' THEN k.awg_public || ' ' || host(k.awg_address)
		ELSE k.vless_id::text END AS key
	FROM entry_grants g
	JOIN entry_relays r ON r.id = g.relay_id
	JOIN hub_access_keys k ON k.account_id = g.account_id
	WHERE r.name = $1
	  AND (g.revoked_at IS NULL
	       OR (g.revoke_reason = 'drained' AND r.status IN ('testing', 'draining')
	           AND g.revoked_at >= r.drained_at))
	  AND ($2 <> 'amneziawg' OR k.awg_public IS NOT NULL)
	  AND EXISTS (SELECT 1 FROM hub_subscriptions s WHERE s.account_id = g.account_id AND ` + liveTerms + `)
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
	if proto == entry.ProtoAmneziaWG {
		rows, err := s.pool.Query(ctx, `SELECT p.public_key || ' ' || host(p.address) FROM relay_capacity_awg_peers p JOIN entry_vantages v USING(slot) WHERE v.status='active' AND v.in_russia ORDER BY p.slot`)
		if err != nil {
			return nil, err
		}
		peers, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, err
		}
		keys = append(keys, peers...)
	}
	if keys == nil {
		keys = []string{}
	}
	return keys, nil
}

// ObservedKeysOf is the keys among KeysOf that are testers' (an account
// holding the whole fleet, hub_accounts.all_relays, is a tester's): the relay
// marks their streams through the cascade for both ends to report
// ([entry.ObservedKeys]; owner's decision 10.11, testers are observed in
// full, other users not at all).
func (s *Service) ObservedKeysOf(ctx context.Context, relay, proto string) ([]string, error) {
	keys, err := s.KeysOf(ctx, relay, proto)
	if err != nil || len(keys) == 0 {
		return []string{}, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT q.key FROM unnest($1::text[]) AS q(key)
		JOIN hub_access_keys k ON k.hy2_auth = q.key OR k.vless_id::text = q.key
		JOIN hub_accounts a ON a.id = k.account_id AND a.all_relays
		ORDER BY 1`, keys)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if out == nil {
		out = []string{}
	}
	return out, err
}

var bootRe = regexp.MustCompile(`^[0-9a-f]{16,64}$`)

// Totals only ever grow within one run of an edge; GREATEST keeps a pulse that
// arrives late from rolling a row back.
const upsertUsage = `
	INSERT INTO hub_key_usage (account_id, relay, boot, bytes_up, bytes_down)
	SELECT k.account_id, $2, $3, $4, $5 FROM hub_access_keys k
	WHERE k.vless_id::text = $1 OR k.hy2_auth = $1 OR k.awg_public = $1
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

// AccountsOf names the account behind each key a relay reports (Hikkari
// v1.04 periods); a key the hub does not know is left out.
func (s *Service) AccountsOf(ctx context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT q.key, k.account_id::text FROM unnest($1::text[]) AS q(key)
		JOIN hub_access_keys k ON k.vless_id::text = q.key OR k.hy2_auth = q.key OR k.awg_public = q.key`, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, account string
		if err := rows.Scan(&key, &account); err != nil {
			return nil, err
		}
		out[key] = account
	}
	return out, rows.Err()
}
