package hikkariobs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// Chart is the admin screen's data: lines over the window's buckets, each a
// question of the observability plan read off hikkari_obs.
type Chart struct {
	Window        string   `json:"window"`
	BucketSeconds int64    `json:"bucketSeconds"`
	Buckets       []string `json:"buckets"`
	Lines         []Line   `json:"lines"`
}

// Line is one series; Values has a value per bucket, null where nothing was
// counted.
type Line struct {
	Key    string     `json:"key"`
	Group  string     `json:"group"` // which chart it belongs to
	Values []*float64 `json:"values"`
}

// totals are sums over every subject: side|path|metric → bucket → value.
type totals map[string]map[int64]float64

// readTotals reads every metric of the window summed over subjects and
// profiles, the way Read sums a step.
func readTotals(ctx context.Context, q Querier, since, until time.Time, step time.Duration) (totals, error) {
	rows, err := q.Query(ctx, `
		SELECT toUnixTimestamp(toStartOfInterval(ts, toIntervalSecond(?))) AS b, side, path, metric,
		       multiIf(endsWith(metric, '.max'), max(value),
		               endsWith(metric, '.p50') OR endsWith(metric, '.p95'), avg(value),
		               sum(value)) AS v
		FROM hikkari_obs
		WHERE ts >= ? AND ts < ? AND subject != 'probe'
		GROUP BY b, side, path, metric`,
		uint32(step/time.Second), since.UTC(), until.UTC())
	if err != nil {
		return nil, fmt.Errorf("hikkari_obs totals: %w", err)
	}
	defer rows.Close()
	out := totals{}
	for rows.Next() {
		var b uint32
		var side, path, metric string
		var v float64
		if err := rows.Scan(&b, &side, &path, &metric, &v); err != nil {
			return nil, err
		}
		key := side + "|" + path + "|" + metric
		if out[key] == nil {
			out[key] = map[int64]float64{}
		}
		out[key][int64(b)] = v
	}
	return out, rows.Err()
}

func (t totals) get(side, path, metric string, b int64) (float64, bool) {
	v, ok := t[side+"|"+path+"|"+metric][b]
	return v, ok
}

// lineSpec is how a line is computed from the totals of one bucket.
type lineSpec struct {
	key, group string
	value      func(t totals, b int64) (float64, bool)
}

func metric(side, path, name string) func(totals, int64) (float64, bool) {
	return func(t totals, b int64) (float64, bool) { return t.get(side, path, name, b) }
}

// ratio is num/den × scale, absent when den is 0 or absent.
func ratio(side, path, num, den string, scale float64) func(totals, int64) (float64, bool) {
	return func(t totals, b int64) (float64, bool) {
		d, ok := t.get(side, path, den, b)
		if !ok || d == 0 {
			return 0, false
		}
		n, _ := t.get(side, path, num, b)
		return n / d * scale, true
	}
}

const (
	direct = "direct"
	trunk  = "trunk"
	none   = ""
)

var lineSpecs = []lineSpec{
	// 1. Does the voice copy by Naive pay: of the call's packets that came,
	// the share only the Naive copy brought, and how often it came first.
	{"saved_down", "dup_saved", ratio(SideClient, none, "dup.only_stream", "dup.fresh", 100)},
	{"first_stream_down", "dup_first", ratio(SideClient, none, "dup.first_stream", "dup.fresh", 100)},
	{"first_stream_up", "dup_first", ratio(SideRelay, direct, "dup.first_stream", "dup.fresh", 100)},
	{"lead_stream_down", "dup_lead", metric(SideClient, none, "dup.lead_stream.p50")},
	{"lead_dgram_down", "dup_lead", metric(SideClient, none, "dup.lead_dgram.p50")},
	// 2. The call detector: calls, and of them the likely false ones.
	{"calls", "calls", metric(SideClient, none, "calls.n")},
	{"calls_short", "calls", metric(SideClient, none, "calls.short")},
	{"calls_one_sided", "calls", metric(SideClient, none, "calls.one_sided")},
	// 3. The trunk's members at the exit: how they ended, their TCP.
	{"trunk_eof", "trunk_ends", metric(SideRelay, trunk, "tunnels.eof")},
	{"trunk_reset", "trunk_ends", metric(SideRelay, trunk, "tunnels.reset")},
	{"trunk_idle", "trunk_ends", metric(SideRelay, trunk, "tunnels.idle")},
	{"trunk_error", "trunk_ends", metric(SideRelay, trunk, "tunnels.error")},
	{"trunk_retrans", "trunk_tcp", ratio(SideRelay, trunk, "tcp.retrans", "tcp.segs_out", 1000)},
	{"trunk_rtt", "trunk_rtt", metric(SideRelay, trunk, "tcp.rtt.p95")},
	// 4. Where a live session breaks: the Naive channel's TCP, the datagram
	// channel's losses, how tunnels ended.
	{"naive_retrans", "naive_tcp", ratio(SideRelay, direct, "tcp.retrans", "tcp.segs_out", 1000)},
	{"naive_rtt", "naive_rtt", metric(SideRelay, direct, "tcp.rtt.p95")},
	{"dgram_loss_up", "quic_loss", ratio(SideClient, none, "quic.datagram.pkts_lost", "quic.datagram.pkts_out", 1000)},
	{"voice_loss_up", "quic_loss", ratio(SideClient, none, "quic.voice.pkts_lost", "quic.voice.pkts_out", 1000)},
	{"dgram_loss_down", "quic_loss", ratio(SideRelay, direct, "quic.datagram.pkts_lost", "quic.datagram.pkts_out", 1000)},
	{"tunnels_eof", "tunnel_ends", metric(SideRelay, direct, "tunnels.eof")},
	{"tunnels_reset", "tunnel_ends", metric(SideRelay, direct, "tunnels.reset")},
	{"tunnels_idle", "tunnel_ends", metric(SideRelay, direct, "tunnels.idle")},
	{"tunnels_revoked", "tunnel_ends", metric(SideRelay, direct, "tunnels.revoked")},
	{"tunnels_stop", "tunnel_ends", metric(SideRelay, direct, "tunnels.relay_stop")},
	{"tunnels_error", "tunnel_ends", metric(SideRelay, direct, "tunnels.error")},
}

var errWindow = errors.New("window: 6h, 24h or 7d")

// BuildChart reads the window into the screen's lines.
func BuildChart(ctx context.Context, q Querier, window string, now time.Time) (Chart, error) {
	span, step := 24*time.Hour, time.Hour
	switch window {
	case "6h":
		span, step = 6*time.Hour, 10*time.Minute
	case "24h", "":
		window = "24h"
	case "7d":
		span, step = 7*24*time.Hour, 6*time.Hour
	default:
		return Chart{}, errWindow
	}
	until := now.Truncate(step).Add(step)
	since := until.Add(-span)
	t, err := readTotals(ctx, q, since, until, step)
	if err != nil {
		return Chart{}, err
	}
	c := Chart{Window: window, BucketSeconds: int64(step / time.Second)}
	var starts []int64
	for b := since; b.Before(until); b = b.Add(step) {
		starts = append(starts, b.Unix())
		c.Buckets = append(c.Buckets, b.UTC().Format(time.RFC3339))
	}
	for _, spec := range lineSpecs {
		line := Line{Key: spec.key, Group: spec.group, Values: make([]*float64, len(starts))}
		for i, b := range starts {
			if v, ok := spec.value(t, b); ok {
				line.Values[i] = &v
			}
		}
		c.Lines = append(c.Lines, line)
	}
	return c, nil
}

// RegisterAdminRoutes mounts GET /api/v1/admin/hikkari/chart?window=24h.
func RegisterAdminRoutes(mux *http.ServeMux, q Querier, auth func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/admin/hikkari/chart", auth(func(w http.ResponseWriter, r *http.Request) {
		c, err := BuildChart(r.Context(), q, r.URL.Query().Get("window"), time.Now())
		if err != nil {
			if errors.Is(err, errWindow) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			applog.Error(r.Context(), "hikkariobs", "chart failed", applog.F("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		writeJSON(w, http.StatusOK, c)
	}))
}
