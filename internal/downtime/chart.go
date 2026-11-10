package downtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// Chart is the product overview's downtime card: per person, over the
// window's buckets, the minutes lost. A sum over everyone would hide the one
// person whose tunnel carried nothing for an hour behind everyone else's zero.
type Chart struct {
	Window        string   `json:"window"`
	BucketSeconds int64    `json:"bucketSeconds"`
	Buckets       []string `json:"buckets"`
	People        []Person `json:"people"`
}

// Person is one install's lines. A bucket the tunnel was never on in is null:
// nothing measured, not zero lost.
type Person struct {
	Install string `json:"install"`
	Name    string `json:"name"`
	// Lost: minutes the tunnel was on and carried nothing (d and c).
	Lost []*float64 `json:"lost"`
	// On: minutes the tunnel was on at all, any state.
	On []*float64 `json:"on"`
	// Net: minutes nothing went and the person's own network was down too (n);
	// not counted as lost.
	Net []*float64 `json:"net"`
	// LostTotal orders the people: the most lost first.
	LostTotal float64 `json:"lostTotal"`
}

// Namer names installs by their alias.
type Namer func(ctx context.Context) (func(install string) string, error)

var errWindow = errors.New("window: 24h, 7d or 30d")

// window is the span and the bucket of a window name.
func window(name string) (string, time.Duration, time.Duration, error) {
	switch name {
	case "24h":
		return name, 24 * time.Hour, time.Hour, nil
	case "7d", "":
		return "7d", 7 * 24 * time.Hour, 6 * time.Hour, nil
	case "30d":
		return name, 30 * 24 * time.Hour, 24 * time.Hour, nil
	}
	return "", 0, 0, errWindow
}

// BuildChart reads the window into per-person lines.
func BuildChart(ctx context.Context, s *Store, name string, now time.Time, names func(string) string) (Chart, error) {
	win, span, step, err := window(name)
	if err != nil {
		return Chart{}, err
	}
	until := now.UTC().Truncate(step).Add(step)
	since := until.Add(-span)
	counts, err := s.Counts(ctx, since, until, step)
	if err != nil {
		return Chart{}, err
	}
	return assemble(win, since, until, step, counts, names), nil
}

func assemble(win string, since, until time.Time, step time.Duration, counts []Count, names func(string) string) Chart {
	c := Chart{Window: win, BucketSeconds: int64(step / time.Second), Buckets: []string{}, People: []Person{}}
	index := map[int64]int{}
	for b := since; b.Before(until); b = b.Add(step) {
		index[b.Unix()] = len(c.Buckets)
		c.Buckets = append(c.Buckets, b.Format(time.RFC3339))
	}
	people := map[string]*Person{}
	add := func(line []*float64, i, n int) {
		if line[i] == nil {
			line[i] = new(float64)
		}
		*line[i] += float64(n)
	}
	for _, k := range counts {
		i, ok := index[k.Bucket.Unix()]
		if !ok {
			continue
		}
		p := people[k.Install]
		if p == nil {
			n := len(c.Buckets)
			p = &Person{Install: k.Install, Lost: make([]*float64, n), On: make([]*float64, n), Net: make([]*float64, n)}
			if names != nil {
				p.Name = names(k.Install)
			}
			if p.Name == "" {
				p.Name = k.Install
			}
			people[k.Install] = p
		}
		add(p.On, i, k.Minutes)
		// A bucket the tunnel was on in has a lost and a net value, zero or not.
		add(p.Lost, i, 0)
		add(p.Net, i, 0)
		switch k.State {
		case Down, CoreDown:
			add(p.Lost, i, k.Minutes)
			p.LostTotal += float64(k.Minutes)
		case NetDown:
			add(p.Net, i, k.Minutes)
		}
	}
	for _, p := range people {
		c.People = append(c.People, *p)
	}
	sort.Slice(c.People, func(a, b int) bool {
		if c.People[a].LostTotal != c.People[b].LostTotal {
			return c.People[a].LostTotal > c.People[b].LostTotal
		}
		return c.People[a].Name < c.People[b].Name
	})
	return c
}

// RegisterAdminRoutes mounts GET /api/v1/admin/product/downtime?window=7d.
func RegisterAdminRoutes(mux *http.ServeMux, s *Store, names Namer, auth func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/admin/product/downtime", auth(func(w http.ResponseWriter, r *http.Request) {
		var name func(string) string
		if names != nil {
			if n, err := names(r.Context()); err == nil {
				name = n
			}
		}
		c, err := BuildChart(r.Context(), s, r.URL.Query().Get("window"), time.Now(), name)
		if err != nil {
			if errors.Is(err, errWindow) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			applog.Error(r.Context(), "downtime", "chart failed", applog.F("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		writeJSON(w, http.StatusOK, c)
	}))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
