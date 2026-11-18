package downtime

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// What a tester's tunnel carried and ran on (hikkari-client
// engine/core/downtime, migration 078).
const (
	bytesPrefix = "[downtime] bytes from="
	statePrefix = "[downtime] state at="
)

// TrafficMinute is one minute of a tester's bytes through the tunnel and past it.
type TrafficMinute struct {
	Install                                    string
	At                                         time.Time
	TunnelUp, TunnelDown, DirectUp, DirectDown int64
}

// ParseBytesLine reads a run of minutes' bytes:
// "[downtime] bytes from=<unix> v=1200,56000,300,900;.;-". "." is a minute
// that carried nothing, stored as zeros; "-" was not measured.
func ParseBytesLine(line, install string) ([]TrafficMinute, bool) {
	i := strings.Index(line, bytesPrefix)
	if i < 0 || install == "" {
		return nil, false
	}
	from, rest, ok := strings.Cut(line[i+len(bytesPrefix):], " v=")
	if !ok {
		return nil, false
	}
	start, err := strconv.ParseInt(from, 10, 64)
	if err != nil || start <= 0 || start%60 != 0 {
		return nil, false
	}
	values, _, _ := strings.Cut(strings.TrimSpace(rest), " ")
	minutes := strings.Split(values, ";")
	if values == "" || len(minutes) > maxRun {
		return nil, false
	}
	var out []TrafficMinute
	for k, v := range minutes {
		at := time.Unix(start+int64(k)*60, 0).UTC()
		switch v {
		case "-":
			continue
		case ".":
			out = append(out, TrafficMinute{Install: install, At: at})
			continue
		}
		parts := strings.Split(v, ",")
		if len(parts) != 4 {
			return nil, false
		}
		var n [4]int64
		for j, p := range parts {
			x, err := strconv.ParseInt(p, 10, 64)
			if err != nil || x < 0 {
				return nil, false
			}
			n[j] = x
		}
		out = append(out, TrafficMinute{Install: install, At: at, TunnelUp: n[0], TunnelDown: n[1], DirectUp: n[2], DirectDown: n[3]})
	}
	return out, true
}

// TesterState is what a tester's tunnel ran on at a moment.
type TesterState struct {
	Install string    `json:"-"`
	At      time.Time `json:"at"`
	// Mode is the routing mode in effect: proxy (only the list through the
	// VPN), bypass (everything but the list), off (everything), "-" unknown.
	Mode    string `json:"mode"`
	Proto   string `json:"proto"`
	Profile string `json:"profile"`
}

// ParseStateLine reads one state line:
// `[downtime] state at=<unix> mode=proxy proto=hikkari profile="амстердам-006"`.
func ParseStateLine(line, install string) (TesterState, bool) {
	i := strings.Index(line, statePrefix)
	if i < 0 || install == "" {
		return TesterState{}, false
	}
	rest := line[i+len(statePrefix):]
	at, rest, ok := strings.Cut(rest, " mode=")
	if !ok {
		return TesterState{}, false
	}
	unix, err := strconv.ParseInt(at, 10, 64)
	if err != nil || unix <= 0 {
		return TesterState{}, false
	}
	mode, rest, ok := strings.Cut(rest, " proto=")
	if !ok {
		return TesterState{}, false
	}
	proto, rest, ok := strings.Cut(rest, " profile=")
	if !ok {
		return TesterState{}, false
	}
	profile, err := strconv.Unquote(strings.TrimSpace(rest))
	if err != nil || mode == "" || proto == "" {
		return TesterState{}, false
	}
	return TesterState{Install: install, At: time.Unix(unix, 0).UTC(), Mode: mode, Proto: proto, Profile: profile}, true
}

// InsertTraffic writes minutes' bytes; a repeated minute takes the newer value.
func (s *Store) InsertTraffic(ctx context.Context, minutes []TrafficMinute) (int, error) {
	if len(minutes) == 0 {
		return 0, nil
	}
	installs := make([]string, len(minutes))
	ats := make([]time.Time, len(minutes))
	tu, td, du, dd := make([]int64, len(minutes)), make([]int64, len(minutes)), make([]int64, len(minutes)), make([]int64, len(minutes))
	for i, m := range minutes {
		installs[i], ats[i] = m.Install, m.At
		tu[i], td[i], du[i], dd[i] = m.TunnelUp, m.TunnelDown, m.DirectUp, m.DirectDown
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO product_tester_traffic_minutes (install_id, minute, tunnel_up, tunnel_down, direct_up, direct_down)
		SELECT * FROM unnest($1::text[], $2::timestamptz[], $3::bigint[], $4::bigint[], $5::bigint[], $6::bigint[])
		ON CONFLICT (install_id, minute) DO UPDATE SET
			tunnel_up = EXCLUDED.tunnel_up, tunnel_down = EXCLUDED.tunnel_down,
			direct_up = EXCLUDED.direct_up, direct_down = EXCLUDED.direct_down`,
		installs, ats, tu, td, du, dd)
	if err != nil {
		return 0, fmt.Errorf("insert traffic minutes: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// InsertStates writes state lines; a repeated moment is the same line.
func (s *Store) InsertStates(ctx context.Context, states []TesterState) (int, error) {
	if len(states) == 0 {
		return 0, nil
	}
	installs := make([]string, len(states))
	ats := make([]time.Time, len(states))
	modes, protos, profiles := make([]string, len(states)), make([]string, len(states)), make([]string, len(states))
	for i, st := range states {
		installs[i], ats[i], modes[i], protos[i], profiles[i] = st.Install, st.At, st.Mode, st.Proto, st.Profile
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO product_tester_state (install_id, at, mode, proto, profile)
		SELECT * FROM unnest($1::text[], $2::timestamptz[], $3::text[], $4::text[], $5::text[])
		ON CONFLICT (install_id, at) DO NOTHING`,
		installs, ats, modes, protos, profiles)
	if err != nil {
		return 0, fmt.Errorf("insert tester states: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// TrafficCount is one install's bytes in one bucket.
type TrafficCount struct {
	Install        string
	Bucket         time.Time
	Tunnel, Direct int64
}

// TrafficCounts reads the bytes in [since, until) grouped into step buckets,
// up and down together.
func (s *Store) TrafficCounts(ctx context.Context, since, until time.Time, step time.Duration) ([]TrafficCount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT install_id,
		       to_timestamp(floor(extract(epoch FROM minute) / $3) * $3) AS bucket,
		       sum(tunnel_up + tunnel_down)::bigint, sum(direct_up + direct_down)::bigint
		FROM product_tester_traffic_minutes
		WHERE minute >= $1 AND minute < $2
		GROUP BY 1, 2`,
		since.UTC(), until.UTC(), int64(step/time.Second))
	if err != nil {
		return nil, fmt.Errorf("read traffic minutes: %w", err)
	}
	defer rows.Close()
	var out []TrafficCount
	for rows.Next() {
		var c TrafficCount
		if err := rows.Scan(&c.Install, &c.Bucket, &c.Tunnel, &c.Direct); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// LatestStates is each tester's last state line.
func (s *Store) LatestStates(ctx context.Context) (map[string]TesterState, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (install_id) install_id, at, mode, proto, profile
		FROM product_tester_state
		ORDER BY install_id, at DESC`)
	if err != nil {
		return nil, fmt.Errorf("read tester states: %w", err)
	}
	defer rows.Close()
	out := map[string]TesterState{}
	for rows.Next() {
		var st TesterState
		if err := rows.Scan(&st.Install, &st.At, &st.Mode, &st.Proto, &st.Profile); err != nil {
			return nil, err
		}
		out[st.Install] = st
	}
	return out, rows.Err()
}
