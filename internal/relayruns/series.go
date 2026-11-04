package relayruns

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// The chart of one knob over time: a line per value of the knob and per
// vantage, so "firefox from SPb" and "firefox at tester-002" stand apart. A
// round's share is what came through; a bucket nobody measured is null, not 0.

// Windows of the chart and their buckets.
var Windows = map[string]struct{ Span, Step time.Duration }{
	"24h": {24 * time.Hour, time.Hour},
	"7d":  {7 * 24 * time.Hour, 6 * time.Hour},
	"30d": {30 * 24 * time.Hour, 24 * time.Hour},
}

// Series is one value of the knob from one vantage.
type Series struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Vantage string `json:"vantage"`
	Source  string `json:"source"`
	// BurstPct is the share of the 50 requests at once that answered.
	BurstPct []*float64 `json:"burstPct"`
	// DlOkPct is the share of rounds whose 1 MB came whole.
	DlOkPct []*float64 `json:"dlOkPct"`
	// AfterPct is the share of the requests after the burst that answered.
	AfterPct []*float64 `json:"afterPct"`
	Rounds   []int      `json:"rounds"`
	// Total is the window's sum: rounds, answered and asked.
	TotalRounds int `json:"totalRounds"`
	TotalBurst  int `json:"totalBurst"`
	TotalAsked  int `json:"totalAsked"`
}

// Chart is the knob's history in one response.
type Chart struct {
	Window        string      `json:"window"`
	BucketSeconds int         `json:"bucketSeconds"`
	Buckets       []time.Time `json:"buckets"`
	Knob          string      `json:"knob"`
	Knobs         []string    `json:"knobs"`
	Profile       string      `json:"profile"`
	Profiles      []string    `json:"profiles"`
	Series        []Series    `json:"series"`
}

// ValueUnknown is a row that does not say the knob's value: an old client
// whose config line was cut before the profile.
const ValueUnknown = "?"

// valueOf is the row's value of the knob on the chart.
func valueOf(r Row, knob string) string {
	if r.Knob == KnobBaseline {
		return "no-protocol"
	}
	if v, ok := r.Params[knob]; ok {
		return v
	}
	if r.Knob == knob && r.Value != "" {
		return r.Value
	}
	return ValueUnknown
}

// BuildChart folds rows into buckets ending at now. name turns a vantage into
// what the chart calls it (an install into its alias).
func BuildChart(rows []Row, window, knob, profile string, now time.Time, name func(string) string) Chart {
	win := Windows[window]
	end := now.Truncate(win.Step)
	n := int(win.Span / win.Step)
	first := end.Add(-time.Duration(n-1) * win.Step)
	c := Chart{Window: window, BucketSeconds: int(win.Step.Seconds()), Knob: knob, Knobs: Knobs, Profile: profile, Series: []Series{}}
	for i := 0; i < n; i++ {
		c.Buckets = append(c.Buckets, first.Add(time.Duration(i)*win.Step))
	}
	type acc struct {
		s                             *Series
		burst, asked, after, afterAll []int
		dlOk                          []int
	}
	byKey := map[string]*acc{}
	for _, r := range rows {
		if profile != "" && r.Profile != profile {
			continue
		}
		i := int(r.MeasuredAt.Sub(first) / win.Step)
		if r.MeasuredAt.Before(first) || i >= n {
			continue
		}
		value := valueOf(r, knob)
		vantage := r.Vantage
		if name != nil && r.Source == SourceTester {
			vantage = name(vantage)
		}
		key := value + " · " + vantage
		a := byKey[key]
		if a == nil {
			a = &acc{
				s:     &Series{Key: key, Value: value, Vantage: vantage, Source: r.Source, Rounds: make([]int, n)},
				burst: make([]int, n), asked: make([]int, n), after: make([]int, n), afterAll: make([]int, n), dlOk: make([]int, n),
			}
			byKey[key] = a
		}
		a.s.Rounds[i]++
		a.s.TotalRounds++
		if r.LoadError {
			// The core did not start: nothing passed, and nothing was asked of
			// the path either. Counted as a round with every request lost, so a
			// value that breaks the core does not look clean.
			a.asked[i] += 50
			a.s.TotalAsked += 50
			continue
		}
		a.burst[i] += r.Burst
		a.asked[i] += r.BurstTotal
		a.after[i] += r.After
		a.afterAll[i] += r.AfterTotal
		if r.DlEnd == "ok" {
			a.dlOk[i]++
		}
		a.s.TotalBurst += r.Burst
		a.s.TotalAsked += r.BurstTotal
	}
	pct := func(num, den int) *float64 {
		if den == 0 {
			return nil
		}
		v := float64(num) * 100 / float64(den)
		v = float64(int(v*10+0.5)) / 10
		return &v
	}
	for _, a := range byKey {
		for i := 0; i < n; i++ {
			a.s.BurstPct = append(a.s.BurstPct, pct(a.burst[i], a.asked[i]))
			a.s.AfterPct = append(a.s.AfterPct, pct(a.after[i], a.afterAll[i]))
			a.s.DlOkPct = append(a.s.DlOkPct, pct(a.dlOk[i], a.s.Rounds[i]))
		}
		c.Series = append(c.Series, *a.s)
	}
	sort.Slice(c.Series, func(i, j int) bool {
		if c.Series[i].Source != c.Series[j].Source {
			return c.Series[i].Source < c.Series[j].Source
		}
		return c.Series[i].Key < c.Series[j].Key
	})
	return c
}

// Namer turns a tester's install into its alias.
type Namer func(ctx context.Context) (func(string) string, error)

// ChartHandler serves the admin chart: ?window=24h|7d|30d&knob=fp&profile=x.
func ChartHandler(s *Store, namer Namer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		window := q.Get("window")
		if window == "" {
			window = "24h"
		}
		win, ok := Windows[window]
		if !ok {
			writeErr(w, http.StatusBadRequest, "window: 24h, 7d or 30d")
			return
		}
		knob := q.Get("knob")
		if knob == "" {
			knob = "fp"
		}
		if !isKnob(knob) {
			writeErr(w, http.StatusBadRequest, "unknown knob")
			return
		}
		now := time.Now()
		since := now.Truncate(win.Step).Add(-win.Span)
		profiles, err := s.Profiles(r.Context(), since)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "read failed")
			return
		}
		profile := q.Get("profile")
		if profile == "" && len(profiles) > 0 {
			profile = profiles[0]
		}
		rows, err := s.Rows(r.Context(), Query{Profile: profile, Since: since, Until: now})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "read failed")
			return
		}
		var name func(string) string
		if namer != nil {
			if name, err = namer(r.Context()); err != nil {
				name = nil
			}
		}
		c := BuildChart(rows, window, knob, profile, now, name)
		c.Profiles = profiles
		writeJSON(w, http.StatusOK, c)
	}
}

// RecordHandler takes the stand's rows: POST a JSON array of rows.
func RecordHandler(s *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var rows []Row
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&rows); err != nil {
			writeErr(w, http.StatusBadRequest, "want a JSON array of rows")
			return
		}
		for i := range rows {
			if rows[i].Source == "" {
				rows[i].Source = SourceStand
			}
			if rows[i].Source != SourceStand {
				writeErr(w, http.StatusBadRequest, "only the stand posts rows; testers' come from their log")
				return
			}
			if err := rows[i].Validate(); err != nil {
				writeErr(w, http.StatusBadRequest, "row "+strconv.Itoa(i)+": "+err.Error())
				return
			}
		}
		n, err := s.Insert(r.Context(), rows)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "insert failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"received": len(rows), "added": n})
	}
}

// ReadHandler gives the agent the raw rows: ?profile=&source=&last=24h.
func ReadHandler(s *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		last := 24 * time.Hour
		if v := q.Get("last"); v != "" {
			d, err := parseSpan(v)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "last: like 30m, 6h, 7d")
				return
			}
			last = d
		}
		source := q.Get("source")
		if source != "" && source != SourceStand && source != SourceTester {
			writeErr(w, http.StatusBadRequest, "source: stand or tester")
			return
		}
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit <= 0 || limit > 5000 {
			limit = 2000
		}
		rows, err := s.Rows(r.Context(), Query{Profile: q.Get("profile"), Source: source, Since: time.Now().Add(-last), Limit: limit})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "read failed")
			return
		}
		if rows == nil {
			rows = []Row{}
		}
		writeJSON(w, http.StatusOK, rows)
	}
}

func parseSpan(v string) (time.Duration, error) {
	if n, ok := cutSuffixInt(v, "d"); ok {
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(v)
}

func cutSuffixInt(v, suffix string) (int, bool) {
	if len(v) < 2 || v[len(v)-len(suffix):] != suffix {
		return 0, false
	}
	n, err := strconv.Atoi(v[:len(v)-len(suffix)])
	return n, err == nil && n > 0 && n <= 90
}

// RegisterOpsRoutes mounts the dataset on the agent's plane.
func RegisterOpsRoutes(mux *http.ServeMux, s *Store, gate func(action string, h http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("POST /api/v1/ops/relay-runs", gate("ops.relay_runs.record", RecordHandler(s)))
	mux.HandleFunc("GET /api/v1/ops/relay-runs", gate("", ReadHandler(s)))
}

// RegisterAdminRoutes mounts the chart on the panel.
func RegisterAdminRoutes(mux *http.ServeMux, s *Store, namer Namer, gate func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/admin/relay-runs/chart", gate(ChartHandler(s, namer)))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
