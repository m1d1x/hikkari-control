package downtime

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ruPrefix starts a run of a tester's minutes to Russian sites
// (hikkari-client engine/core/rureach).
const ruPrefix = "[rureach] minutes from="

// RuMinute is one minute of a tester's own network to Russia: the fastest
// answer, or no answer (MS 0).
type RuMinute struct {
	Install string
	At      time.Time
	MS      int
}

// ParseRuLine reads a run of minutes to Russian sites from one log line.
func ParseRuLine(line, install string) ([]RuMinute, bool) {
	i := strings.Index(line, ruPrefix)
	if i < 0 || install == "" {
		return nil, false
	}
	from, rest, ok := strings.Cut(line[i+len(ruPrefix):], " ms=")
	if !ok {
		return nil, false
	}
	start, err := strconv.ParseInt(from, 10, 64)
	if err != nil || start <= 0 || start%60 != 0 {
		return nil, false
	}
	values, _, _ := strings.Cut(strings.TrimSpace(rest), " ")
	parts := strings.Split(values, ",")
	if values == "" || len(parts) > maxRun {
		return nil, false
	}
	out := make([]RuMinute, 0, len(parts))
	for k, v := range parts {
		at := time.Unix(start+int64(k)*60, 0).UTC()
		switch v {
		case "-":
		case "x":
			out = append(out, RuMinute{Install: install, At: at})
		default:
			ms, err := strconv.Atoi(v)
			if err != nil || ms <= 0 || ms > 60_000 {
				return nil, false
			}
			out = append(out, RuMinute{Install: install, At: at, MS: ms})
		}
	}
	return out, true
}

// InsertRu writes minutes to Russian sites; a repeated minute takes the newer
// value.
func (s *Store) InsertRu(ctx context.Context, minutes []RuMinute) (int, error) {
	if len(minutes) == 0 {
		return 0, nil
	}
	installs := make([]string, len(minutes))
	ats := make([]time.Time, len(minutes))
	ms := make([]*int32, len(minutes))
	for i, m := range minutes {
		installs[i], ats[i] = m.Install, m.At
		if m.MS > 0 {
			v := int32(m.MS)
			ms[i] = &v
		}
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO product_tester_ru_minutes (install_id, minute, ms)
		SELECT * FROM unnest($1::text[], $2::timestamptz[], $3::int[])
		ON CONFLICT (install_id, minute) DO UPDATE SET ms = EXCLUDED.ms`,
		installs, ats, ms)
	if err != nil {
		return 0, fmt.Errorf("insert ru minutes: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// RuCount is one tester's network to Russia over one bucket.
type RuCount struct {
	Install string
	Bucket  time.Time
	// Median of the answered minutes, 0 when none answered.
	MedianMS float64
	Answered int
	Missed   int
}

// RuCounts reads the minutes to Russian sites in [since, until) by step.
func (s *Store) RuCounts(ctx context.Context, since, until time.Time, step time.Duration) ([]RuCount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT install_id,
		       to_timestamp(floor(extract(epoch FROM minute) / $3) * $3) AS bucket,
		       COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY ms) FILTER (WHERE ms IS NOT NULL), 0),
		       count(ms), count(*) - count(ms)
		FROM product_tester_ru_minutes
		WHERE minute >= $1 AND minute < $2
		GROUP BY 1, 2`,
		since.UTC(), until.UTC(), int64(step/time.Second))
	if err != nil {
		return nil, fmt.Errorf("read ru minutes: %w", err)
	}
	defer rows.Close()
	var out []RuCount
	for rows.Next() {
		var c RuCount
		if err := rows.Scan(&c.Install, &c.Bucket, &c.MedianMS, &c.Answered, &c.Missed); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
