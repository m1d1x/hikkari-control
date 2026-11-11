package downtime

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// stallPrefix starts a run of a client's stalls (hikkari-client
// engine/core/downtime): per minute and threshold, the stalls the core timed
// that lasted at least it and their length in ms.
const stallPrefix = "[downtime] stalls from="

// StallMinute is one minute's stalls past one threshold.
type StallMinute struct {
	Install string
	At      time.Time
	BoundMS int
	N       int
	MS      int64
}

// ParseStallLine reads a run of stalls from one log line:
// "[downtime] stalls from=<unix> bounds=300,1000,3000 v=.;2:950,1:1200,0:0;-".
// "." is a minute without a stall, stored as zeros; "-" was not measured.
func ParseStallLine(line, install string) ([]StallMinute, bool) {
	i := strings.Index(line, stallPrefix)
	if i < 0 || install == "" {
		return nil, false
	}
	fields := strings.Fields(line[i+len(stallPrefix):])
	if len(fields) < 3 || !strings.HasPrefix(fields[1], "bounds=") || !strings.HasPrefix(fields[2], "v=") {
		return nil, false
	}
	start, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || start <= 0 || start%60 != 0 {
		return nil, false
	}
	var bounds []int
	for _, b := range strings.Split(strings.TrimPrefix(fields[1], "bounds="), ",") {
		v, err := strconv.Atoi(b)
		if err != nil || v <= 0 || v > 600_000 {
			return nil, false
		}
		bounds = append(bounds, v)
	}
	minutes := strings.Split(strings.TrimPrefix(fields[2], "v="), ";")
	if len(minutes) == 0 || len(minutes) > maxRun {
		return nil, false
	}
	var out []StallMinute
	for k, v := range minutes {
		at := time.Unix(start+int64(k)*60, 0).UTC()
		switch v {
		case "-":
			continue
		case ".":
			for _, b := range bounds {
				out = append(out, StallMinute{Install: install, At: at, BoundMS: b})
			}
			continue
		}
		parts := strings.Split(v, ",")
		if len(parts) != len(bounds) {
			return nil, false
		}
		for j, p := range parts {
			n, ms, ok := strings.Cut(p, ":")
			if !ok {
				return nil, false
			}
			count, err1 := strconv.Atoi(n)
			total, err2 := strconv.ParseInt(ms, 10, 64)
			if err1 != nil || err2 != nil || count < 0 || total < 0 {
				return nil, false
			}
			out = append(out, StallMinute{Install: install, At: at, BoundMS: bounds[j], N: count, MS: total})
		}
	}
	return out, true
}

// InsertStalls writes stalls; a repeated minute takes the newer value.
func (s *Store) InsertStalls(ctx context.Context, minutes []StallMinute) (int, error) {
	if len(minutes) == 0 {
		return 0, nil
	}
	installs := make([]string, len(minutes))
	ats := make([]time.Time, len(minutes))
	bounds := make([]int32, len(minutes))
	ns := make([]int32, len(minutes))
	mss := make([]int64, len(minutes))
	for i, m := range minutes {
		installs[i], ats[i], bounds[i], ns[i], mss[i] = m.Install, m.At, int32(m.BoundMS), int32(m.N), m.MS
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO product_tester_stall_minutes (install_id, minute, bound_ms, n, ms)
		SELECT * FROM unnest($1::text[], $2::timestamptz[], $3::int[], $4::int[], $5::bigint[])
		ON CONFLICT (install_id, minute, bound_ms) DO UPDATE SET n = EXCLUDED.n, ms = EXCLUDED.ms`,
		installs, ats, bounds, ns, mss)
	if err != nil {
		return 0, fmt.Errorf("insert stall minutes: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// StallCount is one install's stalls past one threshold in one bucket.
type StallCount struct {
	Install string
	Bucket  time.Time
	BoundMS int
	N       int64
	MS      int64
}

// StallCounts reads the stalls in [since, until) grouped into step buckets.
func (s *Store) StallCounts(ctx context.Context, since, until time.Time, step time.Duration) ([]StallCount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT install_id,
		       to_timestamp(floor(extract(epoch FROM minute) / $3) * $3) AS bucket,
		       bound_ms, sum(n), sum(ms)
		FROM product_tester_stall_minutes
		WHERE minute >= $1 AND minute < $2
		GROUP BY 1, 2, 3`,
		since.UTC(), until.UTC(), int64(step/time.Second))
	if err != nil {
		return nil, fmt.Errorf("read stall minutes: %w", err)
	}
	defer rows.Close()
	var out []StallCount
	for rows.Next() {
		var c StallCount
		if err := rows.Scan(&c.Install, &c.Bucket, &c.BoundMS, &c.N, &c.MS); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
