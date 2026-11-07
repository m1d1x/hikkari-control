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
		tag, err := s.Pool.Exec(ctx, `WITH saved AS (INSERT INTO relay_capacity (slot, config_key, measured_at, sample, core)
   VALUES ($1,$2,to_timestamp($3),$4,$5) ON CONFLICT(slot,config_key) DO UPDATE SET
   measured_at=excluded.measured_at,sample=excluded.sample,core=excluded.core
   WHERE excluded.measured_at > relay_capacity.measured_at RETURNING *)
 INSERT INTO relay_capacity_history(slot,config_key,measured_at,sample)
 SELECT slot,config_key,measured_at,sample FROM saved ON CONFLICT DO NOTHING`, slot, sample.Key, sample.MeasuredAt, data, core)
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	_, err := s.Pool.Exec(ctx, `WITH cleaned AS (DELETE FROM relay_capacity WHERE measured_at < now()-interval '24 hours') DELETE FROM relay_capacity_history WHERE measured_at < now()-interval '24 hours'`)
	return n, err
}

// Metrics aggregates only active operator-owned RU points. Latest failed reports
// replace successes; history is bounded to 48 half-hour buckets per configuration.
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
	groups := map[string][]Sample{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, err
		}
		var sample Sample
		if err := json.Unmarshal(raw, &sample); err != nil {
			rows.Close()
			return nil, err
		}
		if sample.Validate(time.Now()) == nil {
			groups[sample.Key] = append(groups[sample.Key], sample)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	buckets := map[string]map[int64]map[string]Sample{}
	history, err := s.Pool.Query(ctx, `SELECT c.slot,c.sample FROM relay_capacity_history c JOIN entry_vantages v ON v.slot=c.slot
 WHERE v.status='active' AND v.in_russia AND c.config_key=ANY($1)
 AND c.measured_at>now()-interval '24 hours' ORDER BY c.measured_at`, keys)
	if err != nil {
		return nil, err
	}
	defer history.Close()
	for history.Next() {
		var slot string
		var raw []byte
		if err := history.Scan(&slot, &raw); err != nil {
			return nil, err
		}
		var sample Sample
		if err := json.Unmarshal(raw, &sample); err != nil {
			return nil, err
		}
		bucket := sample.MeasuredAt / 1800 * 1800
		if buckets[sample.Key] == nil {
			buckets[sample.Key] = map[int64]map[string]Sample{}
		}
		if buckets[sample.Key][bucket] == nil {
			buckets[sample.Key][bucket] = map[string]Sample{}
		}
		buckets[sample.Key][bucket][slot] = sample
	}
	if err := history.Err(); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, key := range keys {
		if seen[key] || len(groups[key]) == 0 {
			continue
		}
		seen[key] = true
		m := aggregate(key, groups[key])
		for ts, bySlot := range buckets[key] {
			samples := []Sample{}
			for _, sample := range bySlot {
				samples = append(samples, sample)
			}
			point := aggregate(key, samples)
			m.History = append(m.History, Point{Ts: ts, RTTMS: point.RTTMS, Mbps: point.Mbps})
		}
		sort.Slice(m.History, func(i, j int) bool { return m.History[i].Ts < m.History[j].Ts })
		if len(m.History) > 48 {
			m.History = m.History[len(m.History)-48:]
		}
		out = append(out, m)
	}
	return out, nil
}

func aggregate(key string, samples []Sample) Metric {
	m := Metric{Key: key, Points: len(samples)}
	rates := []float64{}
	rtts := []int{}
	for _, s := range samples {
		if m.MeasuredAt == 0 || s.MeasuredAt < m.MeasuredAt {
			m.MeasuredAt = s.MeasuredAt
		}
		if s.Error == "" && s.Mbps > 0 {
			rates = append(rates, s.Mbps)
			m.Limited = m.Limited || s.Limited
		}
		if s.RTTMS > 0 {
			rtts = append(rtts, s.RTTMS)
		}
	}
	sort.Float64s(rates)
	sort.Ints(rtts)
	if len(rates) > 0 {
		m.Mbps = rates[(len(rates)-1)/2]
	}
	if len(rtts) > 0 {
		m.RTTMS = rtts[(len(rtts)-1)/2]
	}
	m.ExpiresAt = m.MeasuredAt + int64(FreshFor.Seconds())
	return m
}
