package analytics

// entry_load.go reads what the relays carried back out of the dataset, for
// the hub's sizing of the fleet (plan 42 П9.14 demand, П9.15 capacity) and for
// the console's traffic charts.
//
// The relays' counters are totals since the edge process started (migration
// 021), so every reader here starts from the same pulse-by-pulse differencing,
// EntryRelayDeltas, rather than from its own copy of it.

import (
	"context"
	"time"
)

// EntryRelayDeltas differences each relay's cumulative counters pulse by
// pulse. The counters run since the edge process started, so a pair of pulses
// is either one process (the delta is the difference) or straddles a restart
// (the delta is the whole of the newer counter: the bytes before the restart
// are already in the previous delta). A restart is any of: uptime fell, a
// counter fell, or the process is younger than the gap since the previous
// pulse — the last catches a restart after a long silence, where uptime and
// counters have both climbed past their old values again. A relay's first
// pulse in the span has nothing before it and contributes nothing.
//
// dt_s is the time the delta covers: the gap since the previous pulse, or,
// across a restart, only the part of it the process was up. A rate is a sum
// of deltas over a sum of dt_s, never a sum of deltas over a bucket's width —
// a pulse lost to the network leaves the next one carrying two intervals of
// bytes, and dividing those by one interval would read as a burst.
//
// measured says the delta is fit for a rate: it covers at most two minutes
// (four pulses). A delta over a longer silence still counts toward totals (the
// bytes were carried), but its rate is an average over minutes nobody watched
// and must not stand for the load of any one bucket of them.
//
// The one bound parameter is the start of the span.
const EntryRelayDeltas = `
	SELECT
		ts, relay_name, cpu_pct,
		if(seq = 1, 0, if(restarted, bytes_up, toUInt64(bytes_up - prev_up)))       AS d_up,
		if(seq = 1, 0, if(restarted, bytes_down, toUInt64(bytes_down - prev_down))) AS d_down,
		if(seq = 1, 0, if(restarted, least(uptime_s, wall), wall))                AS dt_s,
		seq > 1 AND dt_s BETWEEN 1 AND 120                                        AS measured
	FROM (
		SELECT
			ts, relay_name, uptime_s, bytes_up, bytes_down, cpu_pct, seq,
			toUInt32(greatest(dateDiff('second', prev_ts, ts), 0)) AS wall,
			uptime_s < prev_uptime OR bytes_up < prev_up OR bytes_down < prev_down
				OR toInt64(uptime_s) + 5 < dateDiff('second', prev_ts, ts) AS restarted,
			prev_up, prev_down
		FROM (
			SELECT
				ts, relay_name, uptime_s, bytes_up, bytes_down, cpu_pct,
				lagInFrame(ts) OVER w         AS prev_ts,
				lagInFrame(uptime_s) OVER w   AS prev_uptime,
				lagInFrame(bytes_up) OVER w   AS prev_up,
				lagInFrame(bytes_down) OVER w AS prev_down,
				row_number() OVER w           AS seq
			FROM entry_relay_stats
			WHERE ts >= ?
			WINDOW w AS (PARTITION BY relay_name ORDER BY ts ASC ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)
		)
	)
`

// entryLoadQ is the traffic of each group of relays, bucket by bucket.
//
// A relay's rate in a bucket is its bytes over the seconds those bytes took,
// up and down apart; a group's rate in each direction is the sum of its
// relays' — not its bytes over its relays' summed seconds, which would
// average two relays instead of adding them. The group's load is the busier
// direction: a machine's link is full duplex, and adding the two directions
// would read a machine half as roomy as it is. Its volume is the two added:
// what the hoster bills is what leaves the machine, and a relay sends on
// every byte it receives, in both directions. CPU is the box's own figure,
// which every relay on a box reports, so a group's CPU is the mean of what
// its relays said.
//
// Only measured deltas count (see EntryRelayDeltas): a bucket that holds
// nothing but the far end of a long silence is absent, not averaged.
//
// Parameters, as entryReachRTTQ's: the relay names and the group of each (two
// arrays, position by position), the bucket width in minutes, the start of
// the span, the relay names again.
const entryLoadQ = `
	SELECT
		transform(relay_name, ?, ?, '')                             AS key,
		bucket,
		greatest(sum(rate_up), sum(rate_down)) * 8 / 1000000        AS mbps,
		(sum(rate_up) + sum(rate_down)) * 8 / 1000000               AS volume,
		avg(cpu)                                                    AS cpu
	FROM (
		SELECT
			relay_name,
			toStartOfInterval(ts, toIntervalMinute(?)) AS bucket,
			sumIf(d_up, measured) / sumIf(dt_s, measured)   AS rate_up,
			sumIf(d_down, measured) / sumIf(dt_s, measured) AS rate_down,
			avg(cpu_pct)                                    AS cpu
		FROM (` + EntryRelayDeltas + `)
		WHERE has(?, relay_name)
		GROUP BY relay_name, bucket
		HAVING sumIf(dt_s, measured) > 0
	)
	GROUP BY key, bucket
	ORDER BY key, bucket
`

// EntryLoadRow is one group's traffic in one bucket.
type EntryLoadRow struct {
	Key    string
	Bucket time.Time
	// Mbps is the busier direction; VolumeMbps both, as the hoster bills.
	Mbps       float64
	VolumeMbps float64
	CPUPct     float64
}

// EntryLoad is the traffic of each group of relays since `since`, bucket by
// bucket: `groups` maps a relay name to the group it counts toward (a pool, a
// machine). A relay not in it is not read. A bucket in which a group's relays
// did not pulse is absent rather than zero — nobody measured it.
func (s *Store) EntryLoad(ctx context.Context, since time.Time, bucket time.Duration, groups map[string]string) ([]EntryLoadRow, error) {
	if len(groups) == 0 {
		return nil, nil
	}
	relays, keys := splitGroups(groups)
	rows, err := s.conn.Query(ctx, entryLoadQ, relays, keys, int64(bucket/time.Minute), since, relays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EntryLoadRow
	for rows.Next() {
		var r EntryLoadRow
		if err := rows.Scan(&r.Key, &r.Bucket, &r.Mbps, &r.VolumeMbps, &r.CPUPct); err != nil {
			return nil, err
		}
		r.Bucket = r.Bucket.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// entryReachRTTQ is the p95 RTT clients measured through each group's relays,
// bucket by bucket. Parameters: the bucket width in minutes, the relay names,
// the group of each, the start of the span, the relay names again.
const entryReachRTTQ = `
	SELECT
		transform(relay_name, ?, ?, '')            AS key,
		toStartOfInterval(ts, toIntervalMinute(?)) AS bucket,
		toFloat64(quantile(0.95)(rtt_ms))          AS p95,
		count()                                    AS n
	FROM entry_reach
	WHERE ts >= ? AND outcome = 'relay' AND rtt_ms > 0 AND has(?, relay_name)
	GROUP BY key, bucket
	ORDER BY key, bucket
`

// EntryRTTRow is the RTT people saw through one group in one bucket.
type EntryRTTRow struct {
	Key    string
	Bucket time.Time
	P95Ms  float64
	N      int
}

// EntryReachRTT is the p95 RTT clients reported through each group of relays
// since `since`, bucket by bucket, with how many reports it is drawn from.
// Only sessions a relay carried count: a direct session says nothing about a
// relay's load.
func (s *Store) EntryReachRTT(ctx context.Context, since time.Time, bucket time.Duration, groups map[string]string) ([]EntryRTTRow, error) {
	if len(groups) == 0 {
		return nil, nil
	}
	relays, keys := splitGroups(groups)
	rows, err := s.conn.Query(ctx, entryReachRTTQ, relays, keys, int64(bucket/time.Minute), since, relays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EntryRTTRow
	for rows.Next() {
		var r EntryRTTRow
		var n uint64
		if err := rows.Scan(&r.Key, &r.Bucket, &r.P95Ms, &n); err != nil {
			return nil, err
		}
		r.Bucket = r.Bucket.UTC()
		r.N = int(n)
		out = append(out, r)
	}
	return out, rows.Err()
}

// EntryBenchPeaks is the best downstream a bench run measured through each
// machine since `since`, failed runs left out. A bench is one vantage's
// session through one relay, so the figure is what the machine is known to
// carry at least, not what it can.
func (s *Store) EntryBenchPeaks(ctx context.Context, since time.Time) (map[string]float64, error) {
	rows, err := s.conn.Query(ctx, `
		SELECT box, toFloat64(max(down_mbps)) FROM entry_benches
		WHERE ts >= ? AND error = '' AND box != '' AND down_mbps > 0
		GROUP BY box`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var box string
		var mbps float64
		if err := rows.Scan(&box, &mbps); err != nil {
			return nil, err
		}
		out[box] = mbps
	}
	return out, rows.Err()
}

// splitGroups turns a relay → group map into the two position-matched arrays
// transform() takes.
func splitGroups(groups map[string]string) (relays, keys []string) {
	relays = make([]string, 0, len(groups))
	keys = make([]string, 0, len(groups))
	for r, k := range groups {
		relays = append(relays, r)
		keys = append(keys, k)
	}
	return relays, keys
}
