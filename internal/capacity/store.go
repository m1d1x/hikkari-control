package capacity

import (
	"context"
	"encoding/json"
	"github.com/m1d1x/hikkari-control/internal/db"
	"sort"
	"time"
)

type Store struct{ Pool db.Pool }

// Save is idempotent per point/configuration; old reports cannot replace new ones.
func (s Store) Save(ctx context.Context, slot string, samples []Sample, core string) (int, error) {
	for _, sample := range samples {
		if err := sample.Validate(time.Now()); err != nil {
			return 0, err
		}
	}
	n := 0
	for _, sample := range samples {
		data, err := json.Marshal(sample)
		if err != nil {
			return n, err
		}
		tag, err := s.Pool.Exec(ctx, `INSERT INTO relay_capacity (slot, config_key, measured_at, sample, core)
   VALUES ($1,$2,to_timestamp($3),$4,$5) ON CONFLICT(slot,config_key) DO UPDATE SET
   measured_at=excluded.measured_at,sample=excluded.sample,core=excluded.core
   WHERE excluded.measured_at > relay_capacity.measured_at`, slot, sample.Key, sample.MeasuredAt, data, core)
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM relay_capacity WHERE measured_at < now()-interval '24 hours'`)
	return n, err
}

// Metrics includes only operator-owned active points in Russia. Failed latest
// measurements remove that point's speed instead of retaining a stale success.
func (s Store) Metrics(ctx context.Context, keys []string) ([]Metric, error) {
	out := []Metric{}
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := s.Pool.Query(ctx, `SELECT c.sample FROM relay_capacity c JOIN entry_vantages v ON v.slot=c.slot
  WHERE v.status='active' AND v.in_russia AND c.config_key=ANY($1)
  AND c.measured_at > now()-interval '90 minutes'`, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := map[string][]Sample{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var sample Sample
		if err := json.Unmarshal(raw, &sample); err != nil {
			return nil, err
		}
		if sample.Error == "" && sample.Validate(time.Now()) == nil {
			groups[sample.Key] = append(groups[sample.Key], sample)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, key := range keys {
		samples := groups[key]
		if len(samples) == 0 {
			continue
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i].Mbps < samples[j].Mbps })
		// Median is deliberately conservative against one unusually fast point.
		pick := samples[(len(samples)-1)/2]
		oldest := pick.MeasuredAt
		limited := false
		for _, sample := range samples {
			if sample.MeasuredAt < oldest {
				oldest = sample.MeasuredAt
			}
			limited = limited || sample.Limited
		}
		out = append(out, Metric{Key: key, MeasuredAt: oldest, ExpiresAt: oldest + int64(FreshFor.Seconds()), Mbps: pick.Mbps, Limited: limited, Points: len(samples)})
	}
	return out, nil
}
