package cascadewatch

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// What a listener hears of the trunk's call (hikkari-protocol trunk/hearing.go,
// spec/trunk.md «What a listener hears»; hikkari-client
// plans/2026-11-15-live-udp-two-fates.md, раздел 4): every call's minute line
// carries, up (a listener at the exit) and down (one at the entry), the frames
// judged and, for jitter buffers of 40, 60 and 100 ms, the holes (a frame not
// there at its moment of play), the runs of 2 holes or more and the bad
// seconds. A set of wires (the voice set: the datagrams' wire and the pool)
// has a line of its own, "call set=voice wires=udp,pool from=...", with the
// same counts and how much later the pool's copy came than the datagrams'
// (lag_*_ms). Its rows go to the call layer under target <source>-<set>
// (trunk-voice for the entry, pipe-voice for the stand over hk-pipe).
//
// Counts, not shares, are on the line: a bucket's share is its holes over its
// frames, whatever its length.

var reSetCall = regexp.MustCompile(`nekko-trunk-entry: call set=([a-z0-9]+) wires=(\S+) (from=.*)$`)

// hearingBuffers are the buffers of the line's counts, in ms.
var hearingBuffers = []string{"40", "60", "100"}

// setRedHolesPct: a minute of a set is red when a listener with the
// selector's buffer (60 ms) had this share of holes or more, either way, or
// a single run of 2 (the plan's goal: holes under 1%, no run).
const setRedHolesPct = 1.0

// classifySet turns a set's minute into a row of the call layer, its target
// prefixed with prefix (trunk or pipe); false for any other line.
func classifySet(l journalLine, prefix string) (Run, bool) {
	m := reSetCall.FindStringSubmatch(unwrapLogJSON(l.Msg))
	if m == nil {
		return Run{}, false
	}
	data := callFields(m[3])
	data["set"] = m[1]
	data["wires"] = strings.Split(m[2], ",")
	delete(data, "lost") // a set sends nothing of its own
	ok := true
	var whys []string
	for _, dir := range []string{"up", "down"} {
		frames := num(data, dir+"_frames")
		if frames <= 0 {
			continue
		}
		pct := num(data, dir+"_holes60") * 100 / frames
		runs := num(data, dir+"_runs2_60")
		if pct >= setRedHolesPct || runs > 0 {
			ok = false
			whys = append(whys, fmt.Sprintf("%s: дыр %.2f%%, серий %d", map[string]string{"up": "вверх", "down": "вниз"}[dir], pct, int(runs)))
		}
	}
	run := Run{TS: time.Unix(int64(num(data, "from")), 0).UTC(), Layer: LayerCall, Target: prefix + "-" + m[1], Data: data}
	if has(data, "up_frames") || has(data, "down_frames") {
		run.OK = boolp(ok)
	}
	if !ok {
		data["why"] = "набор " + m[1] + " при буфере 60 мс, " + strings.Join(whys, "; ")
	}
	return run, true
}

// The hearing charts, by group: <what><buffer>_<direction>.
const (
	hearViewTrunk = "trunk" // /protocols/trunk: holes, runs and bad seconds at 60 ms, every call and set
	hearViewVoice = "voice" // the fleet's «Качество голоса»: holes at 40, 60, 100 ms of the voice set
)

var errHearingView = errors.New("view: trunk or voice")

// hearingSpan is a window's span and bucket: a minute, the call's own step,
// up to a day; ten minutes over a week, an hour over the fleet's 30 days.
func hearingSpan(window string) (string, time.Duration, time.Duration, error) {
	switch window {
	case "6h":
		return window, 6 * time.Hour, time.Minute, nil
	case "24h", "":
		return "24h", 24 * time.Hour, time.Minute, nil
	case "7d":
		return window, 7 * 24 * time.Hour, 10 * time.Minute, nil
	case "30d":
		return window, 30 * 24 * time.Hour, time.Hour, nil
	}
	return "", 0, 0, errChartWindow
}

// hearingGroups are what view draws, as (group, key of the count, share or
// sum): a share is the count over the direction's frames, in per cent.
func hearingGroups(view string) [][3]string {
	var out [][3]string
	for _, dir := range []string{"up", "down"} {
		if view == hearViewVoice {
			for _, b := range hearingBuffers {
				out = append(out, [3]string{"holes" + b + "_" + dir, dir + "_holes" + b, "share"})
			}
			continue
		}
		out = append(out,
			[3]string{"holes60_" + dir, dir + "_holes60", "share"},
			[3]string{"runs2_60_" + dir, dir + "_runs2_60", "sum"},
			[3]string{"bad_s60_" + dir, dir + "_bad_s60", "sum"})
	}
	return out
}

// BuildHearingChart is what the listener heard over the window, every call
// and set of entry ("" for every entry) as a line per target: view trunk is
// the trunk page's, view voice only the sets named voice.
func (s *Store) BuildHearingChart(ctx context.Context, entry, window, view string, now time.Time) (WireChart, error) {
	if view == "" {
		view = hearViewTrunk
	}
	if view != hearViewTrunk && view != hearViewVoice {
		return WireChart{}, errHearingView
	}
	window, span, step, err := hearingSpan(window)
	if err != nil {
		return WireChart{}, err
	}
	until := now.Truncate(step).Add(step)
	since := until.Add(-span)
	c := WireChart{Window: window, BucketSeconds: int64(step / time.Second), Marks: []ChartMark{}}
	index := map[int64]int{}
	for b := since; b.Before(until); b = b.Add(step) {
		index[b.Unix()] = len(c.Buckets)
		c.Buckets = append(c.Buckets, b.UTC().Format(time.RFC3339))
	}
	groups := hearingGroups(view)
	keys := []string{"up_frames", "down_frames"}
	for _, g := range groups {
		keys = append(keys, g[1])
	}
	sums := make([]string, len(keys))
	for i, k := range keys {
		sums[i] = fmt.Sprintf("sum((data->>'%s')::float8)", k)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT target, (floor(extract(epoch FROM ts) / $4) * $4)::bigint AS b, `+strings.Join(sums, ", ")+`
		FROM trunk_runs
		WHERE layer = 'call' AND ts >= $1 AND ts < $2 AND ($3 = '' OR entry_box = $3)
		  AND (data ? 'up_frames' OR data ? 'down_frames')
		  AND ($5 = '' OR data->>'set' = $5)
		GROUP BY 1, 2 ORDER BY 1, 2`, since, until, entry, int64(step/time.Second), map[string]string{hearViewVoice: "voice"}[view])
	if err != nil {
		return WireChart{}, fmt.Errorf("hearing: %w", err)
	}
	defer rows.Close()
	lines := map[string]*ChartLine{}
	var order []string
	for rows.Next() {
		var target string
		var b int64
		vals := make([]*float64, len(keys))
		dest := []any{&target, &b}
		for i := range vals {
			dest = append(dest, &vals[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return WireChart{}, err
		}
		i, ok := index[b]
		if !ok {
			continue
		}
		frames := map[string]*float64{"up": vals[0], "down": vals[1]}
		for gi, g := range groups {
			v := vals[2+gi]
			if v == nil {
				continue
			}
			x := *v
			if g[2] == "share" {
				f := frames[strings.SplitN(g[1], "_", 2)[0]]
				if f == nil || *f <= 0 {
					continue
				}
				x = x * 100 / *f
			}
			id := g[0] + "|" + target
			l := lines[id]
			if l == nil {
				l = &ChartLine{Key: target, Group: g[0], Values: make([]*float64, len(c.Buckets))}
				lines[id] = l
				order = append(order, id)
			}
			l.Values[i] = &x
		}
	}
	if err := rows.Err(); err != nil {
		return WireChart{}, err
	}
	for _, id := range order {
		c.Lines = append(c.Lines, *lines[id])
	}
	return c, nil
}
