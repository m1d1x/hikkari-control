package admin

// fleet_cascades.go adds the cascade to the fleet screen (the owner's 14.11:
// loss and ping from the client to Russia and from Russia to the EU relay):
// one card per trunk entry, under the name a subscriber reads for it (the
// exit relay's node label as the cascade link reads it, «нидерланды-102»),
// with its two legs as history on the window's buckets.
//
//   - clientLossPm, clientRttMs: the leg client → entry, the entry's own sum
//     over every client's TCP socket each minute (trunkentry clients.go,
//     layer clients): segments sent again per 1000 sent, and the median over
//     the bucket of the minutes' p50 round trip. No client is named in it.
//   - trunkLossPm, trunkRttMs: the leg entry → exit, the trunk's call with one
//     copy (layer call, target copies1: the route voice takes, a datagram
//     every 20 ms): lost per 1000 sent, and the median of the minutes' p50.
//
// The UDP channel of the clients is their cipher end to end: the entry sees
// no loss in it, so the client leg is TCP's.

import (
	"context"
	"fmt"
	"time"

	"github.com/m1d1x/hikkari-control/internal/subscription"
)

// fleetCascade is one cascade card.
type fleetCascade struct {
	// Label is the name a subscriber reads for the cascade link.
	Label         string `json:"label"`
	Entry         string `json:"entry"`
	EntryLocation string `json:"entryLocation"`
	EntryProvider string `json:"entryProvider"`
	Exit          string `json:"exit"`
	ExitLocation  string `json:"exitLocation"`

	ClientLossPm []*float64 `json:"clientLossPm"`
	ClientRTTMs  []*float64 `json:"clientRttMs"`
	TrunkLossPm  []*float64 `json:"trunkLossPm"`
	TrunkRTTMs   []*float64 `json:"trunkRttMs"`
}

// legBucket is one bucket of one leg of one entry, as the dataset sums it.
type legBucket struct {
	entry, layer string
	at           int64
	bad, total   float64
	rtt          *float64
}

// cascades reads the trunk entries and their legs over the window.
func (h *Handlers) cascades(ctx context.Context, starts []time.Time, now time.Time, bucket time.Duration) ([]fleetCascade, error) {
	rows, err := h.pool.Query(ctx, `
		SELECT t.entry_box, t.exit_box, eb.location, eb.provider, xb.location,
		       coalesce((SELECT coalesce(n.label, n.slug) FROM entry_nodes n
		                 JOIN entry_relays r ON r.id = n.relay_id
		                 WHERE r.box = t.exit_box AND r.status = 'alive'
		                 ORDER BY 1 LIMIT 1), t.exit_box)
		FROM trunk_entries t
		JOIN entry_boxes eb ON eb.name = t.entry_box
		JOIN entry_boxes xb ON xb.name = t.exit_box
		WHERE eb.retired_at IS NULL
		ORDER BY t.entry_box`)
	if err != nil {
		return nil, fmt.Errorf("cascades: %w", err)
	}
	var out []fleetCascade
	for rows.Next() {
		var c fleetCascade
		var exitLabel string
		if err := rows.Scan(&c.Entry, &c.Exit, &c.EntryLocation, &c.EntryProvider, &c.ExitLocation, &exitLabel); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan cascade: %w", err)
		}
		c.Label = subscription.CascadeName(exitLabel)
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return []fleetCascade{}, nil
	}
	sec := int64(bucket.Seconds())
	rows, err = h.pool.Query(ctx, `
		SELECT entry_box, layer, (floor(extract(epoch FROM ts) / $3) * $3)::bigint AS b,
		       sum(CASE WHEN layer = 'clients' THEN (data->>'retrans')::float8 ELSE (data->>'lost')::float8 END),
		       sum(CASE WHEN layer = 'clients' THEN (data->>'segs')::float8 ELSE (data->>'sent')::float8 END),
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY
		           CASE WHEN layer = 'clients' AND jsonb_typeof(data->'rtt_p50') = 'number' THEN (data->>'rtt_p50')::float8
		                WHEN layer = 'call' AND jsonb_typeof(data->'rtt_p50_ms') = 'number' THEN (data->>'rtt_p50_ms')::float8 END)
		FROM trunk_runs
		WHERE vantage = 'entry' AND ts >= $1 AND ts <= $2
		  AND ((layer = 'clients' AND target = 'tcp') OR (layer = 'call' AND target = 'copies1'))
		GROUP BY 1, 2, 3`, starts[0], now, sec)
	if err != nil {
		return nil, fmt.Errorf("cascade legs: %w", err)
	}
	defer rows.Close()
	var legs []legBucket
	for rows.Next() {
		var b legBucket
		if err := rows.Scan(&b.entry, &b.layer, &b.at, &b.bad, &b.total, &b.rtt); err != nil {
			return nil, fmt.Errorf("scan cascade legs: %w", err)
		}
		legs = append(legs, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	foldCascades(out, starts, legs)
	return out, nil
}

// foldCascades lays the legs on the window's buckets: loss per 1000 where
// anything was sent, the round trip where one was heard; null elsewhere.
func foldCascades(cs []fleetCascade, starts []time.Time, legs []legBucket) {
	index := make(map[int64]int, len(starts))
	for i, at := range starts {
		index[at.Unix()] = i
	}
	byEntry := map[string]*fleetCascade{}
	for i := range cs {
		c := &cs[i]
		c.ClientLossPm = make([]*float64, len(starts))
		c.ClientRTTMs = make([]*float64, len(starts))
		c.TrunkLossPm = make([]*float64, len(starts))
		c.TrunkRTTMs = make([]*float64, len(starts))
		byEntry[c.Entry] = c
	}
	for _, b := range legs {
		c, ok := byEntry[b.entry]
		i, in := index[b.at]
		if !ok || !in {
			continue
		}
		loss, rtt := c.TrunkLossPm, c.TrunkRTTMs
		if b.layer == "clients" {
			loss, rtt = c.ClientLossPm, c.ClientRTTMs
		}
		if b.total > 0 {
			pm := b.bad * 1000 / b.total
			if pm < 0 {
				pm = 0
			}
			loss[i] = &pm
		}
		if b.rtt != nil {
			v := *b.rtt
			rtt[i] = &v
		}
	}
}
