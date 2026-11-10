// Package downtime keeps the product's main metric: the minutes the tunnel is
// on and the user's traffic does not go (hikkari-client plans/ROADMAP.md, 3.3).
//
// The client decides each minute (hikkari-client engine/core/downtime) and
// writes runs of them to the tester's log; the ingester moves them into
// tunnel_minutes, and the admin's product overview draws them per person.
package downtime

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/db"
)

// linePrefix starts a run of minutes in a client's log.
const linePrefix = "[downtime] minutes from="

// States a minute can have, as the client writes them. '-' (unseen) is never
// stored.
const (
	Flowing  = 'o'
	Idle     = 'i'
	Down     = 'd'
	CoreDown = 'c'
	NetDown  = 'n'
)

// maxRun bounds one line: a client flushes every ten minutes, a day is
// plenty for anything real.
const maxRun = 24 * 60

// Minute is one minute of one install's tunnel.
type Minute struct {
	Install string
	At      time.Time
	State   byte
}

// ParseLine reads a run of minutes from one log line.
func ParseLine(line, install string) ([]Minute, bool) {
	i := strings.Index(line, linePrefix)
	if i < 0 || install == "" {
		return nil, false
	}
	from, rest, ok := strings.Cut(line[i+len(linePrefix):], " states=")
	if !ok {
		return nil, false
	}
	start, err := strconv.ParseInt(from, 10, 64)
	if err != nil || start <= 0 || start%60 != 0 {
		return nil, false
	}
	states, _, _ := strings.Cut(strings.TrimSpace(rest), " ")
	if states == "" || len(states) > maxRun {
		return nil, false
	}
	out := make([]Minute, 0, len(states))
	for k := 0; k < len(states); k++ {
		s := states[k]
		switch s {
		case Flowing, Idle, Down, CoreDown, NetDown:
			out = append(out, Minute{Install: install, At: time.Unix(start+int64(k)*60, 0).UTC(), State: s})
		case '-':
		default:
			return nil, false
		}
	}
	return out, true
}

// Store is tunnel_minutes.
type Store struct{ pool db.Pool }

// NewStore keeps minutes in pool.
func NewStore(pool db.Pool) *Store { return &Store{pool: pool} }

// Insert writes minutes. A minute already stored takes the newer state: the
// log stream can repeat lines, and they say the same thing.
func (s *Store) Insert(ctx context.Context, minutes []Minute) (int, error) {
	if len(minutes) == 0 {
		return 0, nil
	}
	installs := make([]string, len(minutes))
	ats := make([]time.Time, len(minutes))
	states := make([]string, len(minutes))
	for i, m := range minutes {
		installs[i], ats[i], states[i] = m.Install, m.At, string(m.State)
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO tunnel_minutes (install_id, minute, state)
		SELECT * FROM unnest($1::text[], $2::timestamptz[], $3::text[])
		ON CONFLICT (install_id, minute) DO UPDATE SET state = EXCLUDED.state`,
		installs, ats, states)
	if err != nil {
		return 0, fmt.Errorf("insert tunnel minutes: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// Count is one install's minutes of one bucket, by state.
type Count struct {
	Install string
	Bucket  time.Time
	State   byte
	Minutes int
}

// Counts reads the minutes in [since, until) grouped into step buckets.
func (s *Store) Counts(ctx context.Context, since, until time.Time, step time.Duration) ([]Count, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT install_id,
		       to_timestamp(floor(extract(epoch FROM minute) / $3) * $3) AS bucket,
		       state, count(*)
		FROM tunnel_minutes
		WHERE minute >= $1 AND minute < $2
		GROUP BY 1, 2, 3`,
		since.UTC(), until.UTC(), int64(step/time.Second))
	if err != nil {
		return nil, fmt.Errorf("read tunnel minutes: %w", err)
	}
	defer rows.Close()
	var out []Count
	for rows.Next() {
		var c Count
		var state string
		if err := rows.Scan(&c.Install, &c.Bucket, &state, &c.Minutes); err != nil {
			return nil, err
		}
		if state == "" {
			continue
		}
		c.State = state[0]
		out = append(out, c)
	}
	return out, rows.Err()
}
