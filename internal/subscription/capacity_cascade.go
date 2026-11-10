package subscription

import (
	"context"
	"fmt"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

// capacityCascadeLinks renders, for every way in a cascade account is served
// (entry_cascades: entry, port, exit), the links it is handed through the
// entry, with the relay's own credential in place of an account's key, as
// fleetLinks does for the direct ones. capacity.Key keeps the entry's address
// and port, so these are measured apart from the exit relay served directly:
// the numbers a cascade profile shows are the cascade's, not the direct path's.
// dup is left off: the subscription looks a link's numbers up without it
// (capacity.MeasuredKey).
func (s *Service) capacityCascadeLinks(ctx context.Context, fleet Fleet) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT c.exit_box, c.entry_box, c.entry_port, coalesce(b.location, '')
		FROM entry_cascades c LEFT JOIN entry_boxes b ON b.name = c.exit_box
		ORDER BY 1, 2, 3`)
	if err != nil {
		return nil, fmt.Errorf("subscription: cascades: %w", err)
	}
	type way struct {
		exit, entry, location string
		port                  int
	}
	var ways []way
	for rows.Next() {
		var w way
		if err := rows.Scan(&w.exit, &w.entry, &w.port, &w.location); err != nil {
			rows.Close()
			return nil, err
		}
		ways = append(ways, w)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(ways) == 0 {
		return nil, err
	}
	records, err := fleet.ServingRecords(ctx)
	if err != nil {
		return nil, err
	}
	nodes, err := fleet.NodeNames(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	recs := make([]entry.RelayRecord, 0, len(records))
	for _, raw := range records {
		rec, err := entry.OpenRecord(raw, s.keys, now)
		if err != nil || rec.Proto != entry.ProtoHikkari {
			continue
		}
		recs = append(recs, rec)
	}
	seen := map[string]bool{}
	var links []string
	for _, w := range ways {
		// One way that cannot be resolved (an entry gone from trunk_entries)
		// leaves the others measured.
		via, err := s.cascadeVia(ctx, w.exit, w.entry, w.port, w.location)
		if err != nil {
			applog.Error(ctx, "capacity", "cascade not resolved", applog.F("err", err.Error()))
			continue
		}
		for _, rec := range recs {
			link, ok := via.link(rec, nodes[rec.Name].Label, false)
			if ok && !seen[link] {
				seen[link] = true
				links = append(links, link)
			}
		}
	}
	return links, nil
}
