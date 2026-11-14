package cascadewatch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// The trunk's wires on the admin screen (stage 4, hikkari-client
// plans/2026-11-11-trunk-wires-stage4.md, раздел 5): every call and every
// wire of the dataset as a history, one line per target, and the selector's
// moves as marks on the time axis. Only charts: no number without its line.
//
// Targets keep their meaning from the journal: copies1 and copies2 are the
// pool's own call, wire-<wire> the stand's process for that wire,
// trunk-<wire> the entry's own wire (trunk-pool its pool under the
// selector), pipe-<wire> the stand over hk-pipe (pipe.go). The wire layer names the stand's wire bare and the entry's as
// trunk-<wire>; here both take the call layer's names, so one wire is one
// colour on every chart.

// WireChart is the screen's data.
type WireChart struct {
	Window        string      `json:"window"`
	BucketSeconds int64       `json:"bucketSeconds"`
	Buckets       []string    `json:"buckets"`
	Lines         []ChartLine `json:"lines"`
	// Marks are the selector's moves of a class from wire to wire.
	Marks []ChartMark `json:"marks"`
}

// ChartLine is one target on one chart; Values has a value per bucket, null
// where nothing was measured.
type ChartLine struct {
	Key    string     `json:"key"`   // the target
	Group  string     `json:"group"` // which chart
	Values []*float64 `json:"values"`
}

// ChartMark is a moment on the time axis.
type ChartMark struct {
	At    string `json:"at"`
	Label string `json:"label"`
	Class string `json:"class"`
	Why   string `json:"why"`
}

// The charts, by group.
const (
	ChartRedMinutes = "call_red"    // minutes of the call with a pause over 150 ms or loss over 1%
	ChartGaps60     = "call_gaps60" // pauses over 60 ms
	ChartRTTP99     = "call_rtt"    // the minutes' round trip at p99, median over the bucket
	ChartGapMax     = "call_gap"    // the longest pause of the bucket
	ChartLoss       = "call_loss"   // lost per 1000 sent
	ChartSilence    = "call_silent" // seconds the call heard nothing
	ChartBulkMbps   = "bulk_mbps"   // 1 MiB downloads, median speed
	ChartBulkFailed = "bulk_failed" // 1 MiB downloads that did not all come
	ChartCPU        = "cpu"         // the stand's process on the entry, seconds per minute, median
)

// maxMarks is how many moves of the selector the screen gets, the newest.
const maxMarks = 300

var errChartWindow = errors.New("window: 6h, 24h or 7d")

// chartSpan is a window's span and bucket.
func chartSpan(window string) (string, time.Duration, time.Duration, error) {
	switch window {
	case "6h":
		return window, 6 * time.Hour, 10 * time.Minute, nil
	case "24h", "":
		return "24h", 24 * time.Hour, time.Hour, nil
	case "7d":
		return window, 7 * 24 * time.Hour, 6 * time.Hour, nil
	}
	return "", 0, 0, errChartWindow
}

// wireKey is the call layer's name of a wire layer's target.
func wireKey(target string) string {
	if strings.HasPrefix(target, "trunk-") {
		return target
	}
	return "wire-" + target
}

// BuildWireChart reads the window of entry's rows ("" for every entry).
func (s *Store) BuildWireChart(ctx context.Context, entry, window string, now time.Time) (WireChart, error) {
	window, span, step, err := chartSpan(window)
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
	sec := int64(step / time.Second)

	// The calls: a row is a minute of one call.
	rows, err := s.pool.Query(ctx, `
		SELECT target, (floor(extract(epoch FROM ts) / $4) * $4)::bigint AS b,
		       count(*) FILTER (WHERE ok = false)::float8,
		       sum((data->>'gaps60')::float8),
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY (data->>'rtt_p99_ms')::float8),
		       max((data->>'gap_max_ms')::float8),
		       sum((data->>'lost')::float8), sum((data->>'sent')::float8),
		       sum((data->>'silent_ms')::float8)
		FROM trunk_runs
		WHERE layer = 'call' AND ts >= $1 AND ts < $2 AND ($3 = '' OR entry_box = $3)
		GROUP BY 1, 2 ORDER BY 1, 2`, since, until, entry, sec)
	if err != nil {
		return WireChart{}, fmt.Errorf("calls: %w", err)
	}
	for rows.Next() {
		var target string
		var b int64
		var red float64
		var gaps60, p99, gapMax, lost, sent, silent *float64
		if err := rows.Scan(&target, &b, &red, &gaps60, &p99, &gapMax, &lost, &sent, &silent); err != nil {
			rows.Close()
			return WireChart{}, err
		}
		put(ChartRedMinutes, target, b, &red)
		put(ChartGaps60, target, b, gaps60)
		put(ChartRTTP99, target, b, p99)
		put(ChartGapMax, target, b, gapMax)
		if lost != nil && sent != nil && *sent > 0 {
			pm := *lost * 1000 / *sent
			put(ChartLoss, target, b, &pm)
		}
		if silent != nil {
			secs := *silent / 1000
			put(ChartSilence, target, b, &secs)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return WireChart{}, err
	}

	// The wires: 1 MiB downloads (the stand's "bulk", the entry's
	// "bulk_1m"; the bench's 12 MiB and loads are rare and stay in the
	// dataset) and the stand's CPU per minute.
	rows, err = s.pool.Query(ctx, `
		SELECT target, (floor(extract(epoch FROM ts) / $4) * $4)::bigint AS b,
		       data->>'mode' = 'cpu' AS cpu,
		       count(*) FILTER (WHERE ok = false)::float8,
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY
		           CASE WHEN data->>'mode' = 'cpu' THEN (data->>'cpu_ms')::float8 / 1000
		                WHEN ok AND (data->>'ms')::float8 > 0 THEN (data->>'bytes')::float8 * 8 / (data->>'ms')::float8 / 1000 END)
		FROM trunk_runs
		WHERE layer = 'wire' AND ts >= $1 AND ts < $2 AND ($3 = '' OR entry_box = $3)
		  AND data->>'mode' IN ('bulk', 'bulk_1m', 'cpu')
		GROUP BY 1, 2, 3 ORDER BY 1, 2`, since, until, entry, sec)
	if err != nil {
		return WireChart{}, fmt.Errorf("wires: %w", err)
	}
	for rows.Next() {
		var target string
		var b int64
		var cpu bool
		var failed float64
		var med *float64
		if err := rows.Scan(&target, &b, &cpu, &failed, &med); err != nil {
			rows.Close()
			return WireChart{}, err
		}
		key := wireKey(target)
		if cpu {
			put(ChartCPU, key, b, med)
			continue
		}
		put(ChartBulkFailed, key, b, &failed)
		put(ChartBulkMbps, key, b, med)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return WireChart{}, err
	}

	// The selector's moves, the newest maxMarks of the window.
	rows, err = s.pool.Query(ctx, `
		SELECT ts, data FROM (
			SELECT ts, id, data FROM trunk_runs
			WHERE layer = 'event' AND data->>'kind' = 'route' AND ts >= $1 AND ts < $2 AND ($3 = '' OR entry_box = $3)
			ORDER BY ts DESC, id DESC LIMIT $4) m
		ORDER BY ts, id`, since, until, entry, maxMarks)
	if err != nil {
		return WireChart{}, fmt.Errorf("moves: %w", err)
	}
	for rows.Next() {
		var ts time.Time
		var data map[string]any
		if err := rows.Scan(&ts, &data); err != nil {
			rows.Close()
			return WireChart{}, err
		}
		c.Marks = append(c.Marks, routeMark(ts, data))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return WireChart{}, err
	}

	for _, id := range order {
		c.Lines = append(c.Lines, *lines[id])
	}
	return c, nil
}

// routeMark says a move as the screen's tooltip does.
func routeMark(ts time.Time, data map[string]any) ChartMark {
	str := func(k string) string { v, _ := data[k].(string); return v }
	class, why := str("class"), str("why")
	name := map[string]string{"voice": "голос", "bulk": "объём"}[class]
	if name == "" {
		name = class
	}
	var label string
	switch why {
	case "first":
		label = fmt.Sprintf("%s: на %s", name, str("to"))
	case "red":
		label = fmt.Sprintf("%s: %s красный, на %s", name, str("from"), str("to"))
	case "better":
		label = fmt.Sprintf("%s: %s → %s, лучше %s", name, str("from"), str("to"), str("hold"))
	default:
		label = "маршрут: " + str("detail")
	}
	if str("path") == PathPipe {
		label = "труба, " + label
	}
	return ChartMark{At: ts.UTC().Format(time.RFC3339), Label: label, Class: class, Why: why}
}

// RegisterAdminRoutes mounts GET /api/v1/admin/trunk/wires?window=24h&entry=.
func RegisterAdminRoutes(mux *http.ServeMux, w *Watch, auth func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/admin/trunk/wires", auth(func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		c, err := w.store.BuildWireChart(r.Context(), q.Get("entry"), q.Get("window"), w.now())
		if err != nil {
			if errors.Is(err, errChartWindow) {
				writeJSON(rw, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			applog.Error(r.Context(), "cascadewatch", "wire chart failed", applog.F("err", err.Error()))
			writeJSON(rw, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		writeJSON(rw, http.StatusOK, c)
	}))
}
