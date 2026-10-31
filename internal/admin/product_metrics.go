package admin

// product_metrics.go answers the product overview screen: how many people use
// the client, how many are new, whether they connect, and on what.
//
// # What is counted, exactly
//
// Every figure is read from telemetry_events — the client's own events
// (hikkari-client engine/core/telemetry/contract.go). A person is an install
// ID: a random per-install pseudonym, so "people" is installs, and someone with
// the client on two machines is two. Nothing here can be joined to an account.
//
//   - active: installs that sent any event in the window. The client sends
//     `launch` on every start and `daily_active` once a day it runs, so an
//     install that was used shows up.
//   - fresh: installs whose `install` event (sent once, on first start) fell in
//     the window. returning = active − fresh.
//   - total: every `install` event ever, from the daily rollup — it outlives
//     the 90-day raw rows.
//   - connect: `connect` events; secs is how long StartVPN took, so its median
//     is time-to-connect, not session length.
//   - tunnel minutes: `daily_active.connected_minutes`, one row per install per
//     day, so its median is a typical person's day in the tunnel.
//   - activation: of the installs that are fresh in the window, how many have
//     a `first_connect_ok` — the share that got as far as a working tunnel.
//
// # What it does not count
//
// Landing-page download clicks: the card is on the screen, but the landing
// does not report clicks yet, so Downloads.Clicks is null until it does.
// A figure with no rows behind it is null, never zero: "0% connect" and "no
// connects yet" are different statements.

import (
	"context"
	"log"
	"math"
	"net/http"
	"strconv"
	"time"
)

type productMetrics struct {
	Window        string  `json:"window"`
	BucketSeconds int     `json:"bucketSeconds"`
	Buckets       []int64 `json:"buckets"`

	People     metricsPeople     `json:"people"`
	Connect    metricsConnect    `json:"connect"`
	Activation metricsActivation `json:"activation"`
	Downloads  metricsDownloads  `json:"downloads"`
	// Platforms and Versions split the window's active installs. An install
	// that changed version inside the window counts under each version it ran.
	Platforms []metricsShare `json:"platforms"`
	Versions  []metricsShare `json:"versions"`
}

type metricsPeople struct {
	Active    uint64        `json:"active"`
	Fresh     uint64        `json:"fresh"`
	Returning uint64        `json:"returning"`
	Total     uint64        `json:"total"`
	Series    metricsSeries `json:"series"`
}

type metricsSeries struct {
	Active []uint64 `json:"active"`
	Fresh  []uint64 `json:"fresh"`
}

type metricsConnect struct {
	Attempts uint64 `json:"attempts"`
	// OkPct is null when there were no attempts.
	OkPct *float64 `json:"okPct"`
	// SecsP50 is the median time a successful connect took; null without one.
	SecsP50 *float64 `json:"secsP50"`
	// TunnelMinutesP50 is the median of daily_active.connected_minutes.
	TunnelMinutesP50 *float64 `json:"tunnelMinutesP50"`
}

// metricsDownloads is the download funnel: landing clicks → first launch.
// The first launch is the client's `install` event (its series is
// people.series.fresh). Clicks, ClicksSeries and ConvPct stay null until the
// landing reports its download button.
type metricsDownloads struct {
	Clicks       *uint64  `json:"clicks"`
	ClicksSeries []uint64 `json:"clicksSeries"`
	Installs     uint64   `json:"installs"`
	ConvPct      *float64 `json:"convPct"`
	// Platforms splits the window's first launches by OS.
	Platforms []metricsShare `json:"platforms"`
}

type metricsActivation struct {
	Installs  uint64   `json:"installs"`
	Connected uint64   `json:"connected"`
	Pct       *float64 `json:"pct"`
	// SecsP50 is the median time from install to the first working tunnel.
	SecsP50 *float64 `json:"secsP50"`
}

type metricsShare struct {
	Key      string  `json:"key"`
	Installs uint64  `json:"installs"`
	Pct      float64 `json:"pct"`
}

// ProductMetrics is GET /api/v1/admin/product/metrics?window=24h|7d|30d.
func (h *Handlers) ProductMetrics(w http.ResponseWriter, r *http.Request) {
	if h.analytics == nil {
		http.Error(w, "analytics store not configured", http.StatusServiceUnavailable)
		return
	}
	win, ok := parseProductWindow(r.URL.Query().Get("window"), "7d")
	if !ok {
		http.Error(w, "window is 24h, 7d or 30d", http.StatusBadRequest)
		return
	}
	out, err := h.productMetrics(r.Context(), win, time.Now())
	if err != nil {
		log.Printf("admin product metrics: %v", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, out)
}

func (h *Handlers) productMetrics(ctx context.Context, win productWindow, now time.Time) (*productMetrics, error) {
	since, end := win.span(now)
	out := &productMetrics{
		Window:        win.Name,
		BucketSeconds: int(win.Step.Seconds()),
		Buckets:       win.buckets(since),
		People: metricsPeople{Series: metricsSeries{
			Active: make([]uint64, win.N),
			Fresh:  make([]uint64, win.N),
		}},
		Platforms: []metricsShare{},
		Downloads: metricsDownloads{Platforms: []metricsShare{}},
		Versions:  []metricsShare{},
	}

	rows, err := h.analytics.Query(ctx, `
		SELECT toStartOfInterval(ts, toIntervalSecond(?)) AS b,
			uniq(install), uniqIf(install, name = 'install')
		FROM telemetry_events
		WHERE ts >= ? AND ts < ?
		GROUP BY b`, uint32(win.Step.Seconds()), since, end)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var b time.Time
		var active, fresh uint64
		if err := rows.Scan(&b, &active, &fresh); err != nil {
			rows.Close()
			return nil, err
		}
		if i := win.index(since, b); i >= 0 {
			out.People.Series.Active[i] = active
			out.People.Series.Fresh[i] = fresh
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var attempts, oks, tunnelDays uint64
	var secsP50, tunnelP50 float64
	if err := h.analytics.Row(ctx, `
		SELECT uniq(install), uniqIf(install, name = 'install'),
			countIf(name = 'connect'),
			countIf(name = 'connect' AND JSONExtractBool(fields, 'ok')),
			quantileIf(0.5)(JSONExtractUInt(fields, 'secs'), name = 'connect' AND JSONExtractBool(fields, 'ok')),
			countIf(name = 'daily_active'),
			quantileIf(0.5)(JSONExtractUInt(fields, 'connected_minutes'), name = 'daily_active')
		FROM telemetry_events
		WHERE ts >= ? AND ts < ?`, since, end,
	).Scan(&out.People.Active, &out.People.Fresh, &attempts, &oks, &secsP50, &tunnelDays, &tunnelP50); err != nil {
		return nil, err
	}
	out.People.Returning = out.People.Active - min(out.People.Fresh, out.People.Active)
	out.Connect.Attempts = attempts
	if attempts > 0 {
		out.Connect.OkPct = ptr(float64(oks) / float64(attempts) * 100)
	}
	if oks > 0 {
		out.Connect.SecsP50 = finite(secsP50)
	}
	if tunnelDays > 0 {
		out.Connect.TunnelMinutesP50 = finite(tunnelP50)
	}

	// Activation: the installs born in the window, and whether each has had a
	// first working tunnel since. first_connect_ok is sent once per install,
	// so a row at any time after since is that install's.
	var firstP50 float64
	if err := h.analytics.Row(ctx, `
		SELECT count(), countIf(connected), quantileIf(0.5)(after, connected)
		FROM (
			SELECT install,
				countIf(name = 'first_connect_ok') > 0 AS connected,
				maxIf(JSONExtractUInt(fields, 'secs_since_install'), name = 'first_connect_ok') AS after
			FROM telemetry_events
			WHERE name IN ('install', 'first_connect_ok') AND ts >= ?
			GROUP BY install
			HAVING countIf(name = 'install' AND ts < ?) > 0
		)`, since, end,
	).Scan(&out.Activation.Installs, &out.Activation.Connected, &firstP50); err != nil {
		return nil, err
	}
	if out.Activation.Installs > 0 {
		out.Activation.Pct = ptr(float64(out.Activation.Connected) / float64(out.Activation.Installs) * 100)
	}
	if out.Activation.Connected > 0 {
		out.Activation.SecsP50 = finite(firstP50)
	}

	if err := h.analytics.Row(ctx,
		`SELECT sum(events) FROM telemetry_daily_events WHERE name = 'install'`,
	).Scan(&out.People.Total); err != nil {
		return nil, err
	}

	if out.Platforms, err = h.metricsShares(ctx, "os", false, since, end, out.People.Active, 0); err != nil {
		return nil, err
	}
	if out.Versions, err = h.metricsShares(ctx, "version", false, since, end, out.People.Active, 8); err != nil {
		return nil, err
	}
	out.Downloads.Installs = out.People.Fresh
	if out.Downloads.Platforms, err = h.metricsShares(ctx, "os", true, since, end, out.People.Fresh, 0); err != nil {
		return nil, err
	}
	return out, nil
}

// metricsShares splits the window's installs by one column: the active ones,
// or with fresh only those whose first launch fell in the window. column is
// one of the literals above, never request input.
func (h *Handlers) metricsShares(ctx context.Context, column string, fresh bool, since, end time.Time, active uint64, limit int) ([]metricsShare, error) {
	q := `SELECT ` + column + ` AS k, uniq(install) AS n
		FROM telemetry_events
		WHERE ts >= ? AND ts < ? AND k != ''`
	if fresh {
		q += ` AND name = 'install'`
	}
	q += ` GROUP BY k ORDER BY n DESC, k`
	if limit > 0 {
		q += ` LIMIT ` + strconv.Itoa(limit)
	}
	rows, err := h.analytics.Query(ctx, q, since, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []metricsShare{}
	for rows.Next() {
		var s metricsShare
		if err := rows.Scan(&s.Key, &s.Installs); err != nil {
			return nil, err
		}
		if active > 0 {
			s.Pct = float64(s.Installs) / float64(active) * 100
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func ptr[T any](v T) *T { return &v }

// finite drops the NaN ClickHouse returns for a quantile over no rows.
func finite(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}
