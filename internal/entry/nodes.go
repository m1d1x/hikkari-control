package entry

import (
	"context"
	"github.com/google/uuid"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// NodeSignal is an aggregate verdict, never a raw report or installation id.
// Healthy is explicit: missing or stale evidence must not resolve an outage.
type NodeSignal struct {
	ID      string
	Verdict string
	At      time.Time
}
type NodeSignalSource interface {
	FleetSignals(context.Context, []FleetNode, time.Time) (map[string]NodeSignal, error)
}

func (s *Service) SetNodeSignals(src NodeSignalSource) { s.nodeSignals = src }

// FleetNode is one currently admitted endpoint. Host is decrypted only for
// the in-process external prober, never persisted by the node registry.
type FleetNode struct {
	Slug, Relay, Box, Proto, Host string
	Port                          int
	BoundAt                       time.Time
}

func (s *Service) bindNode(ctx context.Context, tx pgx.Tx, relayID int64, proto, region string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(84002)`); err != nil {
		return err
	}
	// An old slot is reusable only after its relay left service permanently or
	// was evicted on suspicion. Rollout testing/draining keeps its identity.
	_, err := tx.Exec(ctx, `WITH vacant AS (
  SELECT n.slug FROM entry_nodes n JOIN entry_relays r ON r.id=n.relay_id
  WHERE n.proto=$3 AND n.region=$4 AND r.status IN ('dead','suspect')
  ORDER BY n.bound_at, n.slug LIMIT 1 FOR UPDATE OF n
 ), rebound AS (
  UPDATE entry_nodes SET relay_id=$1, bound_at=now()
  WHERE slug=(SELECT slug FROM vacant) RETURNING slug
 )
 INSERT INTO entry_nodes(slug, relay_id, proto, region)
 SELECT $2, $1, $3, $4 WHERE NOT EXISTS(SELECT 1 FROM rebound)
 AND NOT EXISTS(SELECT 1 FROM entry_nodes WHERE relay_id=$1)
 ON CONFLICT (relay_id) DO NOTHING`, relayID, strings.ReplaceAll(uuid.NewString(), "-", ""), proto, region)
	return err
}

// syncFleetNodes also gives a returning second-chance relay its own slot
// if its previous slot has already moved to a replacement.
func (s *Service) syncFleetNodes(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(84002)`); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT r.id,b.proto,coalesce(b.region,'') FROM entry_relays r
 JOIN entry_boxes b ON b.name=r.box LEFT JOIN entry_nodes n ON n.relay_id=r.id
 WHERE r.status='alive' AND b.retired_at IS NULL AND n.slug IS NULL ORDER BY r.id`)
	if err != nil {
		return err
	}
	type missing struct {
		id            int64
		proto, region string
	}
	var relays []missing
	for rows.Next() {
		var r missing
		if err := rows.Scan(&r.id, &r.proto, &r.region); err != nil {
			rows.Close()
			return err
		}
		relays = append(relays, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range relays {
		if err := s.bindNode(ctx, tx, r.id, r.proto, r.region); err != nil {
			return err
		}
	}
	if err := labelFleetNodes(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) FleetNodes(ctx context.Context) ([]FleetNode, error) {
	if err := s.syncFleetNodes(ctx); err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx, `SELECT n.slug, r.name, r.box, n.proto, n.bound_at
 FROM entry_nodes n JOIN entry_relays r ON r.id=n.relay_id
 JOIN entry_boxes b ON b.name=r.box
 WHERE r.status='alive' AND b.retired_at IS NULL ORDER BY n.slug`)
	if err != nil {
		return nil, err
	}
	var nodes []FleetNode
	for rows.Next() {
		var n FleetNode
		if err := rows.Scan(&n.Slug, &n.Relay, &n.Box, &n.Proto, &n.BoundAt); err != nil {
			rows.Close()
			return nil, err
		}
		nodes = append(nodes, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nodes, nil
	}
	relays, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	byName := map[string]PoolRelay{}
	for _, r := range relays {
		byName[r.Name] = r
	}
	for i := range nodes {
		r := byName[nodes[i].Relay]
		nodes[i].Host = r.Address
		nodes[i].Port = r.Port
	}
	return nodes, nil
}

// NodeName is a serving relay's logical node as a subscription names it.
type NodeName struct{ Slug, Label string }

// NodeNames is every serving relay's node, by relay name.
func (s *Service) NodeNames(ctx context.Context) (map[string]NodeName, error) {
	if err := s.syncFleetNodes(ctx); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT r.name,n.slug,coalesce(n.label,'') FROM entry_nodes n JOIN entry_relays r ON r.id=n.relay_id WHERE r.status='alive'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]NodeName{}
	for rows.Next() {
		var relay string
		var n NodeName
		if err := rows.Scan(&relay, &n.Slug, &n.Label); err != nil {
			return nil, err
		}
		out[relay] = n
	}
	return out, rows.Err()
}
