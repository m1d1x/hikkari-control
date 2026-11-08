package subscription

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/capacity"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

// capacityPeer owns one persistent peer per observation point. Admission
// probes keep their reserved peer; no subscriber key is ever used here.
func (s *Service) capacityPeer(ctx context.Context, slot string) (accessKey, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return accessKey{}, err
	}
	defer tx.Rollback(ctx)
	// Lock the slot to serialize first allocation. Inactive/non-RU slots cannot
	// create or receive capacity credentials.
	var active string
	if err = tx.QueryRow(ctx, `SELECT slot FROM entry_vantages WHERE slot=$1 AND status='active' AND in_russia FOR UPDATE`, slot).Scan(&active); err != nil {
		return accessKey{}, err
	}
	var k accessKey
	err = tx.QueryRow(ctx, `SELECT private_key,host(address) FROM relay_capacity_awg_peers WHERE slot=$1`, slot).Scan(&k.awgPrivate, &k.awgAddress)
	if err == pgx.ErrNoRows {
		private, public, keyErr := entry.NewAwgKeyPair()
		if keyErr != nil {
			return k, keyErr
		}
		var n int64
		if err = tx.QueryRow(ctx, `SELECT nextval('hub_awg_address_seq')`).Scan(&n); err != nil {
			return k, err
		}
		address, addrErr := awgAddress(n)
		if addrErr != nil {
			return k, addrErr
		}
		if _, err = tx.Exec(ctx, `INSERT INTO relay_capacity_awg_peers(slot,private_key,public_key,address) VALUES($1,$2,$3,$4)`, slot, private, public, address); err != nil {
			return k, err
		}
		k.awgPrivate, k.awgAddress = private, address
	} else if err != nil {
		return k, err
	}
	if err = tx.Commit(ctx); err != nil {
		return k, fmt.Errorf("capacity peer: %w", err)
	}
	return k, nil
}

// CapacityFleetLinks is only called for a claimed capacity task's own slot.
func (s *Service) CapacityFleetLinks(ctx context.Context, fleet Fleet, slot string) ([]string, error) {
	k, err := s.capacityPeer(ctx, slot)
	if err != nil {
		return nil, err
	}
	links, err := s.fleetLinks(ctx, fleet, &k)
	if err != nil {
		return nil, err
	}
	// The run's samples come back keyed by configuration only; the pair is
	// what lets the fleet screen draw them on the node's card. Not having it
	// costs the screen a line, not the run its links.
	if err := s.rememberCapacityNodes(ctx, links); err != nil {
		applog.Error(ctx, "capacity", "node keys not saved", applog.F("err", err.Error()))
	}
	return links, nil
}

// rememberCapacityNodes writes which node each capacity key is of.
func (s *Service) rememberCapacityNodes(ctx context.Context, links []string) error {
	for _, link := range links {
		key, node := capacity.Key(link), capacity.NodeOf(link)
		if key == "" || node == "" {
			continue
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO relay_capacity_nodes (config_key, node_slug, seen_at) VALUES ($1, $2, now())
 ON CONFLICT (config_key) DO UPDATE SET node_slug = excluded.node_slug, seen_at = excluded.seen_at`, key, node); err != nil {
			return err
		}
	}
	return nil
}

// PrepareCapacityPeers provisions before task claim so the normal edge pulse
// can distribute public keys while the task waits in the queue.
func (s *Service) PrepareCapacityPeers(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT slot FROM entry_vantages WHERE status='active' AND in_russia`)
	if err != nil {
		return err
	}
	slots, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, slot := range slots {
		if _, err := s.capacityPeer(ctx, slot); err != nil {
			return err
		}
	}
	return nil
}
