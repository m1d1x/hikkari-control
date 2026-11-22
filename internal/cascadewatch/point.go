package cascadewatch

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/entry"
)

// The border points (hikkari-client plans/2026-11-19-border-forwarders.md,
// G5): the entry holds one session to its core (the exit box, aeznl1)
// through boxes that only forward the trunk's ports (unl1, ams3). Nothing
// moves between them, so the hub names a point that carries nothing, not a
// receiver the entry is on. The brain says (trunkentry points.go):
//
//	trunk: point <ip> silent: 0 of N members up
//	trunk: point <ip> back: U of N members up after S s
//	trunk: every point silent
//	trunk: a point back after S s of every point silent
//	trunk: points from=<unix> <ip> up=U/N proof_ms=P silent_s=S; ...
//
// A minute is a row of the point layer per point (target the point's box,
// ok when it was silent no second of the minute); a change is an event
// (kind point_silent, point_back, points_silent, points_back). The stand's
// lines are the same with path pipe. The owner hears of the people's points
// (decision 18.11, Telegram): a point silent a whole minute, and every
// point silent a whole minute; the first minute it carries again after.

// LayerPoint is a minute of a border point as the brain saw it (migration 085).
const LayerPoint = "point"

var (
	rePointSilent  = regexp.MustCompile(`trunk: point (\S+) silent: 0 of (\d+) members up$`)
	rePointBack    = regexp.MustCompile(`trunk: point (\S+) back: (\d+) of (\d+) members up after (\d+) s$`)
	rePointsSilent = regexp.MustCompile(`trunk: every point silent$`)
	rePointsBack   = regexp.MustCompile(`trunk: a point back after (\d+) s of every point silent$`)
	rePointsMinute = regexp.MustCompile(`trunk: points from=(\d+) (.+)$`)
	rePointPart    = regexp.MustCompile(`^(\S+) up=(\d+)/(\d+) proof_ms=(-?\d+) silent_s=(\d+)$`)
)

// classifyPoint turns a line about the border points into its rows; names
// says each point's box by its address ("?" for one the hub does not know:
// the boxes' addresses are sealed, the rows carry names).
func classifyPoint(l journalLine, names map[string]string) ([]Run, bool) {
	name := func(ip string) string {
		if n, ok := names[ip]; ok {
			return n
		}
		return "?"
	}
	ev := func(data map[string]any) ([]Run, bool) {
		return []Run{{TS: l.TS, Layer: LayerEvent, Data: data}}, true
	}
	if m := rePointSilent.FindStringSubmatch(l.Msg); m != nil {
		n, _ := strconv.Atoi(m[2])
		return ev(map[string]any{"kind": "point_silent", "point": name(m[1]), "members": n})
	}
	if m := rePointBack.FindStringSubmatch(l.Msg); m != nil {
		up, _ := strconv.Atoi(m[2])
		n, _ := strconv.Atoi(m[3])
		after, _ := strconv.Atoi(m[4])
		return ev(map[string]any{"kind": "point_back", "point": name(m[1]), "up": up, "members": n, "after_s": after})
	}
	if rePointsSilent.MatchString(l.Msg) {
		return ev(map[string]any{"kind": "points_silent"})
	}
	if m := rePointsBack.FindStringSubmatch(l.Msg); m != nil {
		after, _ := strconv.Atoi(m[1])
		return ev(map[string]any{"kind": "points_back", "after_s": after})
	}
	m := rePointsMinute.FindStringSubmatch(l.Msg)
	if m == nil {
		return nil, false
	}
	from, _ := strconv.ParseInt(m[1], 10, 64)
	var runs []Run
	for _, part := range strings.Split(m[2], "; ") {
		p := rePointPart.FindStringSubmatch(part)
		if p == nil {
			continue
		}
		up, _ := strconv.Atoi(p[2])
		n, _ := strconv.Atoi(p[3])
		proof, _ := strconv.Atoi(p[4])
		silent, _ := strconv.Atoi(p[5])
		data := map[string]any{"from": from, "up": up, "members": n, "silent_s": silent}
		if proof >= 0 {
			data["proof_ms"] = proof
		}
		runs = append(runs, Run{TS: time.Unix(from, 0).UTC(), Layer: LayerPoint, Target: name(p[1]), OK: boolp(silent == 0), Data: data})
	}
	return runs, len(runs) > 0
}

// pointMinute is a whole minute of silence: the point carried nothing all of
// it (the brain counts a second per word of the pipe, once a second).
const pointMinute = 60

// pointStreak is one point of an entry as the owner hears of it.
type pointStreak struct {
	since time.Time
	n     int
	told  bool
	last  time.Time
}

// hearPoints takes a minute of the people's border points (the rows of one
// minute line): a point silent a whole minute tells the owner once, every
// point silent a whole minute tells it once more, and the first minute a
// point carries again after tells it is over.
func (w *Watch) hearPoints(ctx context.Context, p entry.TrunkPair, rows []Run) {
	var texts []string
	allDead, seen := true, false
	w.mu.Lock()
	for _, r := range rows {
		if r.Layer != LayerPoint {
			continue
		}
		seen = true
		key := p.Entry + "|point|" + r.Target
		s := w.points[key]
		if s == nil {
			s = &pointStreak{}
			w.points[key] = s
		}
		if !r.TS.After(s.last) {
			allDead = false
			continue
		}
		s.last = r.TS
		dead := num(r.Data, "silent_s") >= pointMinute
		if !dead {
			allDead = false
			if s.told {
				texts = append(texts, fmt.Sprintf("🟢 Точка перехода %s входа людей %s → %s снова везёт с %s UTC, молчала %d мин (с %s UTC).",
					r.Target, p.Entry, p.Exit, r.TS.Format("15:04"), s.n, s.since.Format("15:04")))
			}
			*s = pointStreak{last: r.TS}
			continue
		}
		if s.n == 0 {
			s.since = r.TS
		}
		s.n++
		if !s.told && w.fresh(r.TS) {
			s.told = true
			texts = append(texts, fmt.Sprintf("🔴 Точка перехода %s входа людей %s → %s молчит с %s UTC: ни одного из %d членов магистрали через неё.",
				r.Target, p.Entry, p.Exit, s.since.Format("15:04"), int(num(r.Data, "members"))))
		}
	}
	key := p.Entry + "|points"
	s := w.points[key]
	if s == nil {
		s = &pointStreak{}
		w.points[key] = s
	}
	if seen && rows[0].TS.After(s.last) {
		ts := rows[0].TS
		s.last = ts
		if allDead {
			if s.n == 0 {
				s.since = ts
			}
			s.n++
			if !s.told && w.fresh(ts) {
				s.told = true
				texts = append(texts, fmt.Sprintf("🔴 Молчат все точки перехода входа людей %s → %s (%s) с %s UTC: у людей нет магистрали.",
					p.Entry, p.Exit, strings.Join(p.Points, ", "), s.since.Format("15:04")))
			}
		} else {
			if s.told {
				texts = append(texts, fmt.Sprintf("🟢 Магистраль людей %s → %s снова идёт через точки с %s UTC, все молчали %d мин (с %s UTC).",
					p.Entry, p.Exit, ts.Format("15:04"), s.n, s.since.Format("15:04")))
			}
			*s = pointStreak{last: ts}
		}
	}
	w.mu.Unlock()
	for _, t := range texts {
		w.tellOwner(ctx, p, t)
	}
}

// pathName is the people's trunk as the owner reads it: the entry, the core
// and the points it goes through.
func pathName(p entry.TrunkPair) string {
	if len(p.Points) == 0 {
		return p.Entry + " → " + p.Exit
	}
	return p.Entry + " → " + p.Exit + " через " + strings.Join(p.Points, ", ")
}

const (
	ChartPointSilent = "point_silent" // seconds the point carried nothing
	ChartPointUp     = "point_up"     // members up at the point, the least minute of the bucket
	ChartPointGap    = "point_call_gap"
)

type PointChart struct {
	WireChart
	Routes []PointRoute `json:"routes"`
}

type PointRoute struct {
	Entry  string        `json:"entry"`
	Exit   string        `json:"exit"`
	Points []PointStatus `json:"points"`
}

type PointStatus struct {
	Name   string       `json:"name"`
	Sample *PointSample `json:"sample"`
}

type PointSample struct {
	At      time.Time `json:"at"`
	Up      int       `json:"up"`
	Members int       `json:"members"`
	Silent  int       `json:"silentSeconds"`
}

// BuildPointChart reads the window of the border points: per entry and point
// ("<entry> → <point>", the stand's with " (стенд)") the seconds it was
// silent and its members up, the changes as marks and the matching live calls.
func (s *Store) BuildPointChart(ctx context.Context, window string, now time.Time) (PointChart, error) {
	window, span, step, err := chartSpan(window)
	if err != nil {
		return PointChart{}, err
	}
	switch window {
	case "6h":
		step = time.Minute
	case "24h":
		step = 10 * time.Minute
	case "7d":
		step = time.Hour
	}
	until := now.Truncate(step).Add(step)
	since := until.Add(-span)
	c := PointChart{WireChart: WireChart{Window: window, BucketSeconds: int64(step / time.Second), Marks: []ChartMark{}}}
	index := map[int64]int{}
	for b := since; b.Before(until); b = b.Add(step) {
		index[b.Unix()] = len(c.Buckets)
		c.Buckets = append(c.Buckets, b.UTC().Format(time.RFC3339))
	}
	lines := map[string]*ChartLine{}
	var order []string
	put := func(group, key string, bucket int64, v *float64) {
		i, ok := index[bucket]
		if !ok || v == nil {
			return
		}
		id := group + "|" + key
		l := lines[id]
		if l == nil {
			l = &ChartLine{Key: key, Group: group, Values: make([]*float64, len(c.Buckets))}
			lines[id] = l
			order = append(order, id)
		}
		x := *v
		l.Values[i] = &x
	}
	rows, err := s.pool.Query(ctx, `
		SELECT entry_box || ' → ' || target || CASE WHEN data->>'path' = 'pipe' THEN ' (стенд)' ELSE '' END,
		       (floor(extract(epoch FROM ts) / $3) * $3)::bigint AS b,
		       sum((data->>'silent_s')::float8),
		       min((data->>'up')::float8)
		FROM trunk_runs
		WHERE layer = 'point' AND ts >= $1 AND ts < $2
		GROUP BY 1, 2 ORDER BY 1, 2`, since, until, int64(step/time.Second))
	if err != nil {
		return PointChart{}, fmt.Errorf("points: %w", err)
	}
	for rows.Next() {
		var key string
		var b int64
		var silent, up *float64
		if err := rows.Scan(&key, &b, &silent, &up); err != nil {
			rows.Close()
			return PointChart{}, err
		}
		put(ChartPointSilent, key, b, silent)
		put(ChartPointUp, key, b, up)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return PointChart{}, err
	}
	rows, err = s.pool.Query(ctx, `
		SELECT entry_box || ' → ' || exit_box,
		       (floor(extract(epoch FROM ts) / $3) * $3)::bigint AS b,
		       max((data->>'gap_max_ms')::float8)
		FROM trunk_runs
		WHERE layer = 'call' AND target = 'copies1' AND ts >= $1 AND ts < $2
		GROUP BY 1, 2 ORDER BY 1, 2`, since, until, int64(step/time.Second))
	if err != nil {
		return PointChart{}, fmt.Errorf("point calls: %w", err)
	}
	for rows.Next() {
		var key string
		var bucket int64
		var gap *float64
		if err := rows.Scan(&key, &bucket, &gap); err != nil {
			rows.Close()
			return PointChart{}, err
		}
		put(ChartPointGap, key, bucket, gap)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return PointChart{}, err
	}
	rows, err = s.pool.Query(ctx, `
		SELECT ts, entry_box, data FROM (
			SELECT ts, id, entry_box, data FROM trunk_runs
			WHERE layer = 'event' AND data->>'kind' IN ('point_silent', 'point_back', 'points_silent', 'points_back') AND ts >= $1 AND ts < $2
			ORDER BY ts DESC, id DESC LIMIT $3) m
		ORDER BY ts, id`, since, until, maxMarks)
	if err != nil {
		return PointChart{}, fmt.Errorf("point changes: %w", err)
	}
	for rows.Next() {
		var ts time.Time
		var e string
		var data map[string]any
		if err := rows.Scan(&ts, &e, &data); err != nil {
			rows.Close()
			return PointChart{}, err
		}
		c.Marks = append(c.Marks, pointMark(ts, e, data))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return PointChart{}, err
	}
	for _, id := range order {
		c.Lines = append(c.Lines, *lines[id])
	}
	c.Routes, err = s.currentPointRoutes(ctx, now)
	return c, err
}

// Use the hub's current topology. Removed points and the stand still have
// history, but their samples cannot determine the status of a live route.
func (s *Store) currentPointRoutes(ctx context.Context, now time.Time) ([]PointRoute, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT e.entry_box, e.exit_box, p.name, m.ts,
		       coalesce((m.data->>'up')::int, 0), coalesce((m.data->>'members')::int, 0),
		       coalesce((m.data->>'silent_s')::int, 0)
		FROM trunk_entries e
		CROSS JOIN LATERAL unnest(e.points) WITH ORDINALITY AS p(name, position)
		LEFT JOIN LATERAL (
			SELECT ts, data FROM trunk_runs
			WHERE entry_box = e.entry_box AND exit_box = e.exit_box AND target = p.name
			      AND layer = 'point' AND data->>'path' IS DISTINCT FROM 'pipe'
			      AND ts BETWEEN $1 AND $2
			ORDER BY ts DESC, id DESC LIMIT 1
		) m ON true
		ORDER BY e.entry_box, p.position`, now.Add(-24*time.Hour), now)
	if err != nil {
		return nil, fmt.Errorf("current point routes: %w", err)
	}
	defer rows.Close()
	routes := []PointRoute{}
	for rows.Next() {
		var entryName, exitName string
		var point PointStatus
		var sample PointSample
		var at *time.Time
		if err := rows.Scan(&entryName, &exitName, &point.Name, &at, &sample.Up, &sample.Members, &sample.Silent); err != nil {
			return nil, err
		}
		if at != nil {
			sample.At = at.UTC()
			point.Sample = &sample
		}
		if len(routes) == 0 || routes[len(routes)-1].Entry != entryName {
			routes = append(routes, PointRoute{Entry: entryName, Exit: exitName})
		}
		route := &routes[len(routes)-1]
		route.Points = append(route.Points, point)
	}
	return routes, rows.Err()
}

// pointMark says a change of a point as the screen's tooltip does; why is
// red for a silence and ok for a return.
func pointMark(ts time.Time, e string, data map[string]any) ChartMark {
	str := func(k string) string { v, _ := data[k].(string); return v }
	who := e
	class := "point"
	if str("path") == PathPipe {
		who += " (стенд)"
		class = "point-stand"
	}
	var label, why string
	switch str("kind") {
	case "point_silent":
		label, why = fmt.Sprintf("%s: точка %s молчит, 0 из %v соединений", who, str("point"), data["members"]), "red"
	case "point_back":
		label, why = fmt.Sprintf("%s: точка %s снова везёт, %v из %v соединений, молчала %v с", who, str("point"), data["up"], data["members"], data["after_s"]), "ok"
	case "points_silent":
		label, why = fmt.Sprintf("%s: молчат все точки", who), "red"
	default:
		label, why = fmt.Sprintf("%s: точка вернулась, все молчали %v с", who, data["after_s"]), "ok"
	}
	return ChartMark{At: ts.UTC().Format(time.RFC3339), Label: label, Class: class, Why: why}
}
