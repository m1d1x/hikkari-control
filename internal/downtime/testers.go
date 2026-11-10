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
}

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
	return assembleTesters(win, since, until, step, counts, ru), nil
}

func assembleTesters(win string, since, until time.Time, step time.Duration, counts []Count, ru []RuCount) TesterChart {
	c := TesterChart{Window: win, BucketSeconds: int64(step / time.Second), Buckets: []string{}, Testers: map[string]TesterLines{}}
	index := map[int64]int{}
	for b := since; b.Before(until); b = b.Add(step) {
		index[b.Unix()] = len(c.Buckets)
		c.Buckets = append(c.Buckets, b.Format(time.RFC3339))
	}
	lines := func(install string) TesterLines {
		l, ok := c.Testers[install]
		if !ok {
			n := len(c.Buckets)
			l = TesterLines{Lost: make([]*float64, n), RuMS: make([]*float64, n), RuMissed: make([]*float64, n)}
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
