package hikkariobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/m1d1x/hikkari-control/internal/applog"
)

// Querier reads ClickHouse (analytics.Store).
type Querier interface {
	Query(ctx context.Context, query string, args ...any) (driver.Rows, error)
}

// Query picks series out of hikkari_obs.
type Query struct {
	Since, Until time.Time
	Step         time.Duration
	Side         string // relay, client or both
	Path         string // direct, entry, trunk or all
	Subject      string // an account or an install; all when empty
	Metric       string // a metric or its prefix ("dup.", "quic.voice."); all when empty
}

// Series is one metric of one subject over time.
type Series struct {
	Side    string       `json:"side"`
	Relay   string       `json:"relay"`
	Path    string       `json:"path,omitempty"`
	Subject string       `json:"subject"`
	Who     string       `json:"who,omitempty"`
	Metric  string       `json:"metric"`
	Points  [][2]float64 `json:"points"` // unix seconds, value
}

// maxPoints bounds one answer.
const maxPoints = 50000

// Read sums each metric per step: counts add up, a period's p50 and p95
// average over the periods of the step (an approximation: the buckets stay
// on the ends), a max is the step's max.
func Read(ctx context.Context, q Querier, query Query) ([]Series, error) {
	if query.Step < time.Minute {
		query.Step = time.Minute
	}
	rows, err := q.Query(ctx, `
		SELECT toUnixTimestamp(toStartOfInterval(ts, toIntervalSecond(?))) AS b, side, relay, path, subject, metric,
		       multiIf(endsWith(metric, '.max'), max(value),
		               endsWith(metric, '.p50') OR endsWith(metric, '.p95'), avg(value),
		               sum(value)) AS v
		FROM hikkari_obs
		WHERE ts >= ? AND ts < ?
		  AND (? = '' OR side = ?) AND (? = '' OR path = ?) AND (? = '' OR subject = ?)
		  AND (? = '' OR startsWith(metric, ?))
		GROUP BY b, side, relay, path, subject, metric
		ORDER BY side, relay, path, subject, metric, b
		LIMIT ?`,
		uint32(query.Step/time.Second), query.Since.UTC(), query.Until.UTC(),
		query.Side, query.Side, query.Path, query.Path, query.Subject, query.Subject,
		query.Metric, query.Metric, maxPoints)
	if err != nil {
		return nil, fmt.Errorf("hikkari_obs: %w", err)
	}
	defer rows.Close()
	var out []Series
	for rows.Next() {
		var b uint32
		var side, relay, path, subject, metric string
		var v float64
		if err := rows.Scan(&b, &side, &relay, &path, &subject, &metric, &v); err != nil {
			return nil, err
		}
		n := len(out)
		if n == 0 || out[n-1].Side != side || out[n-1].Relay != relay || out[n-1].Path != path || out[n-1].Subject != subject || out[n-1].Metric != metric {
			out = append(out, Series{Side: side, Relay: relay, Path: path, Subject: subject, Metric: metric})
			n++
		}
		out[n-1].Points = append(out[n-1].Points, [2]float64{float64(b), v})
	}
	return out, rows.Err()
}

// Namer names subjects: installs by their alias, accounts by their name.
type Namer func(ctx context.Context) (func(side, subject string) string, error)

// RegisterOpsRoutes mounts the agent's view: GET /api/v1/ops/hikkari?last=24h
// [&step=1h][&side=relay][&path=direct][&who=tester-001|account][&metric=dup.].
func RegisterOpsRoutes(mux *http.ServeMux, q Querier,
	resolve func(ctx context.Context, side, ref string) (string, error), names Namer,
	gate func(action string, h http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/ops/hikkari", gate("", func(w http.ResponseWriter, r *http.Request) {
		v := r.URL.Query()
		span, err := parseSpan(v.Get("last"), 24*time.Hour)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "last: 6h, 24h, 7d, 30d"})
			return
		}
		step, err := parseSpan(v.Get("step"), stepFor(span))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "step: 10m, 1h, 1d"})
			return
		}
		now := time.Now()
		query := Query{Since: now.Add(-span), Until: now, Step: step, Side: v.Get("side"), Path: v.Get("path"), Metric: v.Get("metric")}
		if query.Side != "" && query.Side != SideRelay && query.Side != SideClient {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "side: relay or client"})
			return
		}
		if who := v.Get("who"); who != "" {
			if query.Subject, err = resolve(r.Context(), query.Side, who); err != nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
				return
			}
		}
		series, err := Read(r.Context(), q, query)
		if err != nil {
			applog.Error(r.Context(), "hikkariobs", "read failed", applog.F("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		if name, err := names(r.Context()); err == nil {
			for i := range series {
				series[i].Who = name(series[i].Side, series[i].Subject)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"since": query.Since.UTC(), "step_s": int64(step / time.Second), "series": series, "guide": guide,
		})
	}))
}

// guide is what an answer's numbers mean, for whoever reads it.
const guide = "Hikkari v1.04 (hikkari-protocol spec/v1.md). side relay: what the relay saw, subject = account, path direct (client's own connection), entry (came by the cascade's trunk: its tcp/quic numbers are the box's), trunk (the trunk's members). side client: what the core saw, subject = install, relay = profile tag. Counts are sums per step; *.p50/*.p95 average the periods' percentiles, *.max is the step's max. tcp.*: TCP_INFO of the Naive connection (rtt ms, retrans_pm = resent per 1000 sent). quic.datagram/quic.voice: QUIC connections (loss_pm per 1000). dup (relay: up, client: down): first_dgram/first_stream = which channel's copy came first, only_stream = packets the Naive copy saved, both = both came, lead_* = by how many ms the first beat the second. calls: the client's detected calls, short (<10 s) and one_sided are likely false. tunnels.*: how CONNECT tunnels ended."

func stepFor(span time.Duration) time.Duration {
	switch {
	case span <= 6*time.Hour:
		return 10 * time.Minute
	case span <= 48*time.Hour:
		return time.Hour
	default:
		return 24 * time.Hour
	}
}

func parseSpan(v string, fallback time.Duration) (time.Duration, error) {
	if v == "" {
		return fallback, nil
	}
	if n := len(v); n > 1 && v[n-1] == 'd' {
		days, err := strconv.Atoi(v[:n-1])
		if err != nil || days <= 0 || days > 180 {
			return 0, errors.New("bad span")
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 || d > 180*24*time.Hour {
		return 0, errors.New("bad span")
	}
	return d, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// SortedMetrics lists the metric names of series, for a chart's picker.
func SortedMetrics(series []Series) []string {
	seen := map[string]bool{}
	for _, s := range series {
		seen[s.Metric] = true
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}
