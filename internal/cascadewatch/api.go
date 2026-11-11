package cascadewatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/stand"
)

// RegisterOpsRoutes mounts GET /api/v1/ops/trunk: the cascade dataset over a
// window, with a summary per entry (scripts/ops trunk, MCP trunk).
func RegisterOpsRoutes(mux *http.ServeMux, w *Watch, gate func(action string, h http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/ops/trunk", gate("", w.opsHandler))
	mux.HandleFunc("POST /api/v1/ops/cascade-load", gate("ops.cascade.load", w.loadHandler))
}

// loadRequest is a synthetic users run asked for by the operator.
type loadRequest struct {
	Slot    string `json:"slot"`
	Entry   string `json:"entry"`
	Users   int    `json:"users"`
	Minutes int    `json:"minutes"`
	GamePPS int    `json:"game_pps"`
	Echo    string `json:"echo"`
}

// loadHandler queues a synthetic users task (stand/load.go) on a point in
// Russia that is not the entry itself: a point on the entry's box would load
// the very CPU the run is there to hear.
func (w *Watch) loadHandler(rw http.ResponseWriter, r *http.Request) {
	var req loadRequest
	if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(rw, http.StatusBadRequest, map[string]string{"error": "тело запроса: " + err.Error()})
		return
	}
	echo, err := netip.ParseAddrPort(req.Echo)
	if err != nil {
		writeJSON(rw, http.StatusBadRequest, map[string]string{"error": "echo: нужен адрес ip:port"})
		return
	}
	spec := stand.LoadSpec{Users: req.Users, Minutes: req.Minutes, GamePPS: req.GamePPS, Echo: echo}
	if err := spec.Validate(); err != nil {
		writeJSON(rw, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	ctx := r.Context()
	p, err := w.pair(ctx, req.Entry)
	if err != nil {
		writeJSON(rw, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	ru, err := w.ruSlots(ctx)
	if err != nil {
		writeJSON(rw, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	if !ru[req.Slot] {
		writeJSON(rw, http.StatusBadRequest, map[string]string{"error": "точка " + req.Slot + " не активна или не в РФ"})
		return
	}
	box, err := w.fleet.VantageBox(ctx, req.Slot)
	if err != nil {
		writeJSON(rw, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	if box == p.Entry {
		writeJSON(rw, http.StatusBadRequest, map[string]string{"error": "точка " + req.Slot + " стоит на самом входе: нагрузка грузила бы тот процессор, который меряем"})
		return
	}
	id, err := w.queue.QueueLoad(ctx, req.Slot, p.Entry, spec)
	if errors.Is(err, stand.ErrLoadBusy) {
		writeJSON(rw, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(rw, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	writeJSON(rw, http.StatusAccepted, map[string]any{"id": id, "slot": req.Slot, "entry": p.Entry, "values": spec.Values(),
		"guide": "Точка возьмёт задачу в течение минуты; строки придут в слой load инструмента trunk через minutes плюс около минуты. Сравнивай с слоем call тех же минут."})
}

func (w *Watch) opsHandler(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	last := 6 * time.Hour
	if v := q.Get("last"); v != "" {
		d, err := parseWindow(v)
		if err != nil {
			writeJSON(rw, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		last = d
	}
	until := w.now()
	since := until.Add(-last)
	rows, err := w.store.Rows(r.Context(), Query{Since: since, Until: until.Add(time.Minute), Entry: q.Get("entry")})
	if err != nil {
		writeJSON(rw, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	out := map[string]any{
		"window":  map[string]string{"from": since.UTC().Format(time.RFC3339), "to": until.UTC().Format(time.RFC3339)},
		"summary": Summarize(rows),
		"guide": "Слои: entry (строка stats входа раз в минуту, счётчики с запуска входа), event (член пула умер: why stalled/broken, " +
			"after_s; смена эпохи выхода; старт входа), vps (бокс: steal, load, память, потери интерфейса d_*), path (голый TCP с 12 source port " +
			"и ICMP к порту релея; vantage entry: вход, иначе контрольная точка), trunk (куски provoke 48 × 256 КиБ, single и pool; target direct: " +
			"прямо через границу, entry: через магистраль; busy: шёл опыт 1, раунд пропущен), protocol (ядро клиента на Hikkari-релее выхода: " +
			"1 МиБ, залп 50, 3 после, звонок 30 с: gaps60, gap_max; target direct или entry; vantage: слот точки), verdict (первый красный слой " +
			"раунда снизу: vps, path, trunk, protocol; notes: красное не по вине каскада). Все строки раунда несут один round.",
	}
	layer := q.Get("layer")
	if layer != "" || q.Get("rows") == "1" {
		limit := 2000
		if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= maxRows {
			limit = v
		}
		var keep []Run
		for _, row := range rows {
			if layer == "" || row.Layer == layer {
				keep = append(keep, row)
			}
		}
		if len(keep) > limit {
			out["truncated"] = len(keep) - limit
			keep = keep[len(keep)-limit:]
		}
		out["rows"] = keep
	}
	writeJSON(rw, http.StatusOK, out)
}

func parseWindow(v string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(v, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days < 1 || days > 30 {
			return 0, fmt.Errorf("окно %q: от 1d до 30d", v)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 || d > 30*24*time.Hour {
		return 0, fmt.Errorf("окно %q: 30m, 6h, 7d", v)
	}
	return d, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// counters of the minute line whose growth over a window the summary counts.
var counters = []string{"joined", "stalled", "broken", "resent", "port_kills", "streams_opened", "reset", "up", "down", "dgrams_out", "dgrams_in", "copies"}

// Summarize reads a window of the dataset per entry: the pool's counters
// grown over it (an entry restart starts its counters again and is counted
// from zero), the members that died and after how long, the rounds' verdicts,
// and each layer's readings by vantage and target.
func Summarize(rows []Run) map[string]any {
	type acc struct {
		minutes         int
		membersMin      float64
		membersMax      float64
		grown           map[string]float64
		prev            map[string]any
		restarts        int
		rttP50          []float64
		rttMax          float64
		deadPortsMax    float64
		events          map[string]int
		downAfter       []float64
		downWhy         map[string]int
		verdicts        int
		verdictsOK      int
		firstRed        map[string]int
		reds            []map[string]any
		layers          map[string]*layerAcc
		firstTS, lastTS time.Time
	}
	per := map[string]*acc{}
	get := func(r Run) *acc {
		key := r.Entry + " → " + r.Exit
		if per[key] == nil {
			per[key] = &acc{membersMin: -1, grown: map[string]float64{}, events: map[string]int{}, downWhy: map[string]int{},
				firstRed: map[string]int{}, layers: map[string]*layerAcc{}}
		}
		return per[key]
	}
	for _, r := range rows {
		a := get(r)
		switch r.Layer {
		case LayerEntry:
			a.minutes++
			if a.firstTS.IsZero() {
				a.firstTS = r.TS
			}
			a.lastTS = r.TS
			m := num(r.Data, "members")
			if a.membersMin < 0 || m < a.membersMin {
				a.membersMin = m
			}
			if m > a.membersMax {
				a.membersMax = m
			}
			if a.prev != nil {
				restarted := num(r.Data, "joined") < num(a.prev, "joined")
				if restarted {
					a.restarts++
				}
				for _, k := range counters {
					if restarted {
						a.grown[k] += num(r.Data, k)
					} else {
						a.grown[k] += num(r.Data, k) - num(a.prev, k)
					}
				}
			}
			a.prev = r.Data
			if has(r.Data, "rtt_p50_ms") {
				a.rttP50 = append(a.rttP50, num(r.Data, "rtt_p50_ms"))
			}
			if v := num(r.Data, "rtt_max_ms"); v > a.rttMax {
				a.rttMax = v
			}
			if v := num(r.Data, "dead_ports"); v > a.deadPortsMax {
				a.deadPortsMax = v
			}
		case LayerEvent:
			kind, _ := r.Data["kind"].(string)
			a.events[kind]++
			if kind == "member_down" {
				a.downAfter = append(a.downAfter, num(r.Data, "after_s"))
				why, _ := r.Data["why"].(string)
				a.downWhy[why]++
			}
		case LayerVerdict:
			a.verdicts++
			if r.OK != nil && *r.OK {
				a.verdictsOK++
				continue
			}
			first, _ := r.Data["first_red"].(string)
			a.firstRed[first]++
			if len(a.reds) < 50 {
				a.reds = append(a.reds, map[string]any{"round": r.Round, "first_red": first, "red": r.Data["red"]})
			}
		default:
			key := r.Layer + " " + r.Vantage + " " + r.Target
			if a.layers[key] == nil {
				a.layers[key] = &layerAcc{metrics: map[string][]float64{}}
			}
			a.layers[key].add(r)
		}
	}
	out := map[string]any{}
	for key, a := range per {
		s := map[string]any{}
		if a.minutes > 0 {
			grown := map[string]int64{}
			for k, v := range a.grown {
				grown[k] = int64(v)
			}
			s["entry"] = map[string]any{
				"minutes": a.minutes, "from": a.firstTS, "to": a.lastTS, "members_min": a.membersMin, "members_max": a.membersMax,
				"grown": grown, "restarts": a.restarts, "dead_ports_max": a.deadPortsMax,
				"rtt_p50_ms_median": median(a.rttP50), "rtt_max_ms": a.rttMax,
			}
		}
		if len(a.events) > 0 {
			ev := map[string]any{"by_kind": a.events}
			if len(a.downAfter) > 0 {
				sort.Float64s(a.downAfter)
				ev["member_down"] = map[string]any{"n": len(a.downAfter), "why": a.downWhy,
					"after_s_min": a.downAfter[0], "after_s_median": median(a.downAfter), "after_s_max": a.downAfter[len(a.downAfter)-1]}
			}
			s["events"] = ev
		}
		if a.verdicts > 0 {
			s["verdicts"] = map[string]any{"rounds": a.verdicts, "ok": a.verdictsOK, "first_red": a.firstRed, "red_rounds": a.reds}
		}
		layers := map[string]any{}
		for k, l := range a.layers {
			layers[k] = l.summary()
		}
		if len(layers) > 0 {
			s["layers"] = layers
		}
		out[key] = s
	}
	return out
}

// layerAcc gathers one layer at one vantage and target.
type layerAcc struct {
	n, ok, red, skipped int
	metrics             map[string][]float64
	whys                map[string]int
	// The call's: its silence over the window, and its longest pauses with
	// their time, to lay beside the pool's deaths and the entry's pauses.
	silentMs float64
	worst    []map[string]any
}

// callWorstKept is how many of the window's longest pauses of a call the
// summary lists.
const callWorstKept = 10

// What each layer's summary keeps, by median and worst.
var layerMetrics = map[string][]string{
	LayerVPS:      {"steal_pct", "load1", "busy_pct", "mem_avail_mb", "retrans_pct", "d_rx_drop", "conntrack", "tcp_estab"},
	LayerPath:     {"opened", "connect_p50_ms", "ping_avg_ms", "ping_max_ms", "ping_recv"},
	LayerTrunk:    {"delivered", "first_ok", "attempts", "chunk_p50_ms", "chunk_p99_ms", "goodput_mbps"},
	LayerProtocol: {"dl", "burst", "after", "voice_got", "gaps60", "gaps150", "gap_max_ms", "voice_rtt_p50_ms", "voice_jitter_ms"},
	LayerCall:     {"sent", "lost", "rtt_p50_ms", "rtt_p99_ms", "rtt_max_ms", "gaps60", "gaps150", "gap_max_ms", "silent_ms"},
	LayerWire:     {"bytes", "ms", "mbps", "cpu_ms", "steal_pct", "busy_pct"},
}

func (l *layerAcc) add(r Run) {
	l.n++
	switch {
	case r.OK == nil:
		l.skipped++
	case *r.OK:
		l.ok++
	default:
		l.red++
		if why, _ := r.Data["why"].(string); why != "" {
			if l.whys == nil {
				l.whys = map[string]int{}
			}
			if len(l.whys) < 20 {
				l.whys[why]++
			}
		}
	}
	if r.Layer == LayerCall {
		l.silentMs += num(r.Data, "silent_ms")
		if ws, ok := r.Data["worst"].([]any); ok {
			for _, g := range ws {
				if m, ok := g.(map[string]any); ok {
					l.worst = append(l.worst, m)
				}
			}
		} else if ws, ok := r.Data["worst"].([]map[string]any); ok {
			l.worst = append(l.worst, ws...)
		}
		sort.SliceStable(l.worst, func(i, j int) bool { return num(l.worst[i], "ms") > num(l.worst[j], "ms") })
		if len(l.worst) > callWorstKept {
			l.worst = l.worst[:callWorstKept]
		}
	}
	mode, _ := r.Data["mode"].(string)
	for _, k := range layerMetrics[r.Layer] {
		if has(r.Data, k) {
			key := k
			if mode != "" {
				key = mode + "." + k
			}
			l.metrics[key] = append(l.metrics[key], num(r.Data, k))
		}
	}
}

func (l *layerAcc) summary() map[string]any {
	m := map[string]any{}
	for k, vs := range l.metrics {
		sort.Float64s(vs)
		m[k] = map[string]float64{"min": vs[0], "median": median(vs), "max": vs[len(vs)-1]}
	}
	s := map[string]any{"n": l.n, "ok": l.ok, "red": l.red, "metrics": m}
	if l.skipped > 0 {
		s["skipped"] = l.skipped
	}
	if len(l.whys) > 0 {
		s["why_red"] = l.whys
	}
	if l.silentMs > 0 || len(l.worst) > 0 {
		s["silent_ms_total"] = l.silentMs
		s["worst"] = l.worst
	}
	return s
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	return c[len(c)/2]
}
