package downtime

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// TesterChart is the admin's testers screen: for each tester, over the
// window's buckets, the minutes lost and their own network to Russia, side by
// side, so a broken tunnel reads apart from a broken home network.
type TesterChart struct {
	Window        string   `json:"window"`
	BucketSeconds int64    `json:"bucketSeconds"`
	Buckets       []string `json:"buckets"`
	// Testers are keyed by install; one absent has no minutes in the window.
	Testers map[string]TesterLines `json:"testers"`
	// States is each tester's last state line, whenever it was: what the
	// tunnel runs on now, or ran on when it was last on.
	States map[string]TesterState `json:"states"`
}

// TesterLines are one tester's lines, a value per bucket, null where nothing
// was measured.
type TesterLines struct {
	// Lost: minutes the tunnel was on and carried nothing (d and c).
	Lost []*float64 `json:"lost"`
	// RuMS: the median answer of Russian sites over the direct path.
	RuMS []*float64 `json:"ruMs"`
	// RuMissed: minutes no Russian site answered.
	RuMissed []*float64 `json:"ruMissed"`
	// StallMS: the length in ms of the stalls the client's core timed that
	// lasted at least StallShortMS; null where nothing was measured.
	StallMS []*float64 `json:"stallMs"`
	// StallLongMS: the same for the stalls of at least StallLongBoundMS.
	StallLongMS []*float64 `json:"stallLongMs"`
	// TunnelMB and DirectMB: megabytes up and down together through the
	// tunnel and past it (client newer than 1.0.92); null where the client
	// wrote nothing.
	TunnelMB []*float64 `json:"tunnelMb"`
	DirectMB []*float64 `json:"directMb"`
}

// The thresholds the testers screen draws, of the ones the client counts.
const (
	StallShortMS     = 300
	StallLongBoundMS = 1000
)

// BuildTesterChart reads the window into per-tester lines.
func BuildTesterChart(ctx context.Context, s *Store, name string, now time.Time) (TesterChart, error) {
	win, span, step, err := window(name)
	if err != nil {
		return TesterChart{}, err
	}
	until := now.UTC().Truncate(step).Add(step)
	since := until.Add(-span)
	counts, err := s.Counts(ctx, since, until, step)
	if err != nil {
		return TesterChart{}, err
	}
	ru, err := s.RuCounts(ctx, since, until, step)
	if err != nil {
		return TesterChart{}, err
	}
	stalls, err := s.StallCounts(ctx, since, until, step)
	if err != nil {
		return TesterChart{}, err
	}
	traffic, err := s.TrafficCounts(ctx, since, until, step)
	if err != nil {
		return TesterChart{}, err
	}
	states, err := s.LatestStates(ctx)
	if err != nil {
		return TesterChart{}, err
	}
	c := assembleTesters(win, since, until, step, counts, ru, stalls, traffic)
	c.States = states
	return c, nil
}

func assembleTesters(win string, since, until time.Time, step time.Duration, counts []Count, ru []RuCount, stalls []StallCount, traffic []TrafficCount) TesterChart {
	c := TesterChart{
		Window: win, BucketSeconds: int64(step / time.Second), Buckets: []string{},
		Testers: map[string]TesterLines{}, States: map[string]TesterState{},
	}
	index := map[int64]int{}
	for b := since; b.Before(until); b = b.Add(step) {
		index[b.Unix()] = len(c.Buckets)
		c.Buckets = append(c.Buckets, b.Format(time.RFC3339))
	}
	lines := func(install string) TesterLines {
		l, ok := c.Testers[install]
		if !ok {
			n := len(c.Buckets)
			l = TesterLines{
				Lost: make([]*float64, n), RuMS: make([]*float64, n), RuMissed: make([]*float64, n),
				StallMS: make([]*float64, n), StallLongMS: make([]*float64, n),
				TunnelMB: make([]*float64, n), DirectMB: make([]*float64, n),
			}
			c.Testers[install] = l
		}
		return l
	}
	add := func(line []*float64, i int, v float64) {
		if line[i] == nil {
			line[i] = new(float64)
		}
		*line[i] += v
	}
	for _, k := range counts {
		i, ok := index[k.Bucket.Unix()]
		if !ok {
			continue
		}
		l := lines(k.Install)
		add(l.Lost, i, 0)
		if k.State == Down || k.State == CoreDown {
			add(l.Lost, i, float64(k.Minutes))
		}
	}
	for _, k := range ru {
		i, ok := index[k.Bucket.Unix()]
		if !ok {
			continue
		}
		l := lines(k.Install)
		add(l.RuMissed, i, float64(k.Missed))
		if k.Answered > 0 {
			add(l.RuMS, i, k.MedianMS)
		}
	}
	for _, k := range stalls {
		i, ok := index[k.Bucket.Unix()]
		if !ok {
			continue
		}
		switch k.BoundMS {
		case StallShortMS:
			add(lines(k.Install).StallMS, i, float64(k.MS))
		case StallLongBoundMS:
			add(lines(k.Install).StallLongMS, i, float64(k.MS))
		}
	}
	const mb = 1 << 20
	for _, k := range traffic {
		i, ok := index[k.Bucket.Unix()]
		if !ok {
			continue
		}
		l := lines(k.Install)
		add(l.TunnelMB, i, float64(k.Tunnel)/mb)
		add(l.DirectMB, i, float64(k.Direct)/mb)
	}
	return c
}

// registerTesterRoutes mounts GET /api/v1/admin/product/testers?window=24h.
func registerTesterRoutes(mux *http.ServeMux, s *Store, auth func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/admin/product/testers", auth(func(w http.ResponseWriter, r *http.Request) {
		c, err := BuildTesterChart(r.Context(), s, r.URL.Query().Get("window"), time.Now())
		if err != nil {
			if errors.Is(err, errWindow) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			applog.Error(r.Context(), "downtime", "tester chart failed", applog.F("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		writeJSON(w, http.StatusOK, c)
	}))
}
