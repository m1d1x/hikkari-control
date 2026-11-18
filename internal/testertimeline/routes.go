package testertimeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/cascadewatch"
	"github.com/m1d1x/hikkari-control/internal/support"
)

// maxSpan bounds one timeline: two days of a tester's log is the whole of the
// spike's last check.
const maxSpan = 48 * time.Hour

// maxChunks bounds the log read for one timeline.
const maxChunks = 20000

// Sources are what a timeline reads.
type Sources struct {
	Testers support.TesterStore
	Trunk   *cascadewatch.Store
}

// RegisterOpsRoutes mounts GET /api/v1/ops/timeline?who=&last= (or from=&to=,
// RFC 3339): one tester's minutes beside our side's events.
func RegisterOpsRoutes(mux *http.ServeMux, src Sources, gate func(action string, h http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/ops/timeline", gate("", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		from, to, err := window(q.Get("last"), q.Get("from"), q.Get("to"), time.Now())
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		install, alias, err := src.Testers.ResolveInstall(r.Context(), q.Get("who"))
		switch {
		case errors.Is(err, support.ErrAliasNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "install not found"})
			return
		case errors.Is(err, support.ErrAmbiguousInstall):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "reference matches several installs, be more specific"})
			return
		case err != nil:
			applog.Error(r.Context(), "testertimeline", "resolve failed", applog.F("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		out, err := build(r.Context(), src, install, from, to)
		if err != nil {
			applog.Error(r.Context(), "testertimeline", "build failed", applog.F("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		out["who"], out["install_id"] = alias, install
		writeJSON(w, http.StatusOK, out)
	}))
}

func build(ctx context.Context, src Sources, install string, from, to time.Time) (map[string]any, error) {
	// A chunk arrives after its lines: read a little past the window's end,
	// and from a little before its start for runs of minutes that began then.
	chunks, err := src.Testers.ReadChunks(ctx, install, from.Add(-15*time.Minute), to.Add(15*time.Minute), maxChunks)
	if err != nil {
		return nil, err
	}
	var lines [][]Line
	for _, c := range chunks {
		var ls []Line
		for _, t := range strings.Split(c.Body, "\n") {
			if t != "" {
				ls = append(ls, Line{Text: t, ReceivedAt: c.ReceivedAt})
			}
		}
		lines = append(lines, ls)
	}
	offset := Offset(lines)
	b := NewBuilder(from, to)
	for _, ls := range lines {
		for _, l := range ls {
			b.AddTesterLine(l.Text, offset)
		}
	}
	var ours []Our
	deaths := map[string]map[time.Time]int{}
	for _, layer := range []string{"event", "call"} {
		rows, err := src.Trunk.Rows(ctx, cascadewatch.Query{Since: from, Until: to, Layer: layer})
		if err != nil {
			return nil, err
		}
		for _, run := range rows {
			if deaths[run.Entry] == nil {
				deaths[run.Entry] = map[time.Time]int{}
			}
			if o, ok := OursFromTrunk(run.Entry, run.TS, run.Layer, run.Target, run.Data, deaths[run.Entry]); ok {
				ours = append(ours, o)
			}
		}
	}
	for entry, d := range deaths {
		ours = append(ours, DeathsAsOurs(entry, d)...)
	}
	b.AddOurs(ours)
	return map[string]any{
		"from":         from.UTC(),
		"to":           to.UTC(),
		"offset_hours": offset.Hours(),
		"guide":        "minutes with something in them, UTC. tester: state (d, c, n of the main metric), stalls_1s and stalls_3s (the tunnel's stalls of the minute), voice (the probe's series), pauses (hikkari periods: pauses the client handed out over 60 and 150 ms by lane, stream = ended by a TCP copy), events (clicks, hot reloads, the auto group, health). ours: the entry's events (generations, receiver and exit moves, the selector's routes, dead members) and the people's trunk call's red minutes. near: our events within 2 minutes of a minute the tunnel stalled 1 s or more. The tester's local time is turned to UTC by offset_hours, guessed from when their chunks arrived.",
		"minutes":      b.Minutes(ours),
	}, nil
}

// window reads last (30m, 6h, 2d) or from and to (RFC 3339); the default is
// the last 6 hours.
func window(last, fromS, toS string, now time.Time) (time.Time, time.Time, error) {
	to := now
	if toS != "" {
		t, err := time.Parse(time.RFC3339, toS)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("to: RFC 3339")
		}
		to = t
	}
	from := to.Add(-6 * time.Hour)
	switch {
	case fromS != "":
		t, err := time.Parse(time.RFC3339, fromS)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("from: RFC 3339")
		}
		from = t
	case last != "":
		d, err := span(last)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		from = to.Add(-d)
	}
	if !from.Before(to) || to.Sub(from) > maxSpan {
		return time.Time{}, time.Time{}, errors.New("the window must be positive and at most 48h")
	}
	return from, to, nil
}

func span(v string) (time.Duration, error) {
	if strings.HasSuffix(v, "d") {
		d, err := time.ParseDuration(strings.TrimSuffix(v, "d") + "h")
		return d * 24, err
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, errors.New("last: 30m, 6h, 2d")
	}
	return d, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
