package subscription

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/entry"
)

// The cascade watch's protocol layer (internal/cascadewatch): an observation
// point's prober runs the client core on the exit's Hikkari relay twice in one
// task, straight and through the trunk entry, under an account of the hub's
// own (all_relays, so the relay knows its key). The links are named in ASCII
// by path and relay, the names the dataset keeps: a row says which path it
// measured without a subscriber's words in it.

// CascadeProbePath names the two links of a cascade probe.
const (
	CascadeProbeDirect = "direct"
	CascadeProbeEntry  = "entry"
)

// CascadeProbeLabel is the label, and the dataset's profile, of the link on
// path to relay.
func CascadeProbeLabel(path, relay string) string { return "cascade-" + path + "-" + relay }

// CascadeProbeLinks are account's links to exitBox's Hikkari relays (in the
// pool or out of it), each
// straight and through the trunk entry on entryBox at port, with v1.02's
// duplicated UDP as testers' clients get it.
func (s *Service) CascadeProbeLinks(ctx context.Context, account, entryBox, exitBox string, port int) ([]string, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT id FROM hub_accounts WHERE name = $1 AND all_relays`, account).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w %q: no such account holding the whole fleet", ErrName, account)
	}
	if err != nil {
		return nil, err
	}
	via, ok := s.batch.(CascadeVia)
	if !ok {
		return nil, ErrNoCascadeSource
	}
	address, relays, err := via.CascadeVia(ctx, entryBox, exitBox)
	if err != nil {
		return nil, err
	}
	key, err := s.accessKey(ctx, id)
	if err != nil {
		return nil, err
	}
	// The exit's relays as they stand, not as the pool hands them out: a
	// relay a canary took out of the pool is still the exit, and the watch
	// measures it.
	boxes, ok := s.batch.(interface {
		BoxRecords(ctx context.Context, box string) ([][]byte, error)
	})
	if !ok {
		return nil, ErrNoCascadeSource
	}
	records, err := boxes.BoxRecords(ctx, exitBox)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var links []string
	for _, raw := range records {
		rec, err := entry.OpenRecord(raw, s.keys, now)
		if err != nil || rec.Proto != entry.ProtoHikkari || !relays[rec.Name] {
			continue
		}
		rec = withKey(rec, key)
		for _, path := range []string{CascadeProbeDirect, CascadeProbeEntry} {
			r := rec
			if path == CascadeProbeEntry {
				r.Address, r.Port = address, port
			}
			link, err := HikkariLink(r, true)
			if err != nil {
				return nil, fmt.Errorf("relay %s: %w", rec.Name, err)
			}
			u, err := url.Parse(link)
			if err != nil {
				return nil, err
			}
			u.Fragment = CascadeProbeLabel(path, rec.Name)
			links = append(links, u.String())
		}
	}
	if len(links) == 0 {
		return nil, fmt.Errorf("%w: account %s is served none of %s's Hikkari relays", ErrName, account, exitBox)
	}
	return links, nil
}
