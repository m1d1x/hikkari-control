package subscription

import (
	"context"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

// Fleet is the entry pool as the matrix stand sees it: every relay a
// subscriber can be handed, and the logical node each one serves
// (entry.Service in production).
type Fleet interface {
	ServingRecords(ctx context.Context) ([][]byte, error)
	NodeNames(ctx context.Context) (map[string]entry.NodeName, error)
}

// FleetLinks renders every relay of fleet as a subscriber of no known network
// would get it — the node's name and knobs on the link, the sing-box form of
// Hysteria2 — but with the relay's own shared credential in place of an
// account's key. The stand measures what the fleet hands out, not what one
// account happened to draw: a relay outside the stand's batch would otherwise
// go to its first subscriber with knobs nobody has measured.
//
// AmneziaWG is left out: its link needs a peer key of an account, and the
// stand measures TLS profiles.
func (s *Service) FleetLinks(ctx context.Context, fleet Fleet) ([]string, error) {
	records, err := fleet.ServingRecords(ctx)
	if err != nil {
		return nil, err
	}
	nodes, err := fleet.NodeNames(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	links := make([]string, 0, len(records))
	for _, raw := range records {
		rec, err := entry.OpenRecord(raw, s.keys, now)
		if err != nil {
			applog.Error(ctx, "subscription", "fleet relay record not opened", applog.F("err", err.Error()))
			continue
		}
		render := Link
		switch rec.Proto {
		case entry.ProtoAmneziaWG:
			continue
		case entry.ProtoHysteria2:
			render = SingBoxHysteriaLink
		}
		link, err := render(rec)
		if err != nil {
			continue
		}
		links = append(links, namedLink(link, nodes[rec.Name], coversOf(rec)))
	}
	return links, nil
}
