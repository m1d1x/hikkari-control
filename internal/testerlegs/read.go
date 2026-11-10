package testerlegs

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

// Leg is what one end said of one observation, at the mark and at the end.
type Leg struct {
	Box  string `json:"box"`
	Mark *Row   `json:"mark,omitempty"`
	End  *Row   `json:"end,omitempty"`
}

// Observation is one tester's stream or flow, both ends side by side.
type Observation struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Start   time.Time `json:"start"`
	Subject string    `json:"subject"`
	Who     string    `json:"who,omitempty"`
	Entry   *Leg      `json:"entry,omitempty"`
	Exit    *Leg      `json:"exit,omitempty"`
}

// maxRead bounds one answer.
const maxRead = 20000

// Read is the observations whose exit row came since, of subject (all
// when empty), newest first.
func Read(ctx context.Context, q Querier, since time.Time, subject string) ([]Observation, error) {
	rows, err := q.Query(ctx, `
		SELECT ts, id, side, box, subject, kind, phase, port, age_ms, up, down, chunks, resent, carriers, deaths,
		       max_wait_ms, dg_out, dg_in, dg_copy, end_reason, have_tcp, client_rtt_ms, client_rttvar, client_retrans,
		       client_lost, client_unacked, client_cwnd, client_segs_out, client_segs_in
		FROM tester_legs
		WHERE ts >= ? AND id IN (SELECT id FROM tester_legs WHERE ts >= ? AND side = 'exit' AND (? = '' OR subject = ?))
		ORDER BY id, ts
		LIMIT ?`, since.UTC(), since.UTC(), subject, subject, maxRead)
	if err != nil {
		return nil, fmt.Errorf("tester_legs: %w", err)
	}
	defer rows.Close()
	byID := map[string]*Observation{}
	for rows.Next() {
		var r Row
		var tcp uint8
		if err := rows.Scan(&r.TS, &r.ID, &r.Side, &r.Box, &r.Subject, &r.Kind, &r.Phase, &r.Port, &r.AgeMs, &r.Up, &r.Down,
			&r.Chunks, &r.Resent, &r.Carriers, &r.Deaths, &r.MaxWaitMs, &r.DgOut, &r.DgIn, &r.DgCopy, &r.End, &tcp,
			&r.ClientRTTMs, &r.ClientRTTVar, &r.ClientRetrans, &r.ClientLost, &r.ClientUnacked, &r.ClientCwnd,
			&r.ClientSegsOut, &r.ClientSegsIn); err != nil {
			return nil, err
		}
		r.HaveTCP = tcp == 1
		o := byID[r.ID]
		if o == nil {
			o = &Observation{ID: r.ID, Kind: r.Kind, Start: r.TS.Add(-time.Duration(r.AgeMs) * time.Millisecond)}
			byID[r.ID] = o
		}
		leg := &o.Entry
		if r.Side == SideExit {
			leg = &o.Exit
			o.Subject = r.Subject
		}
		if *leg == nil {
			*leg = &Leg{Box: r.Box}
		}
		row := r
		if r.Phase == "end" {
			(*leg).End = &row
		} else {
			(*leg).Mark = &row
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Observation, 0, len(byID))
	for _, o := range byID {
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.After(out[j].Start) })
	return out, nil
}

// MarshalJSON keeps a row's answer to the numbers a reader needs.
func (r Row) MarshalJSON() ([]byte, error) {
	m := map[string]any{
		"ts": r.TS, "port": r.Port, "age_ms": r.AgeMs, "up": r.Up, "down": r.Down, "chunks": r.Chunks, "resent": r.Resent,
		"carriers": r.Carriers, "deaths": r.Deaths, "max_wait_ms": r.MaxWaitMs, "dg_out": r.DgOut, "dg_in": r.DgIn,
		"dg_copy": r.DgCopy,
	}
	if r.End != "" {
		m["end"] = r.End
	}
	if r.HaveTCP {
		m["client"] = map[string]any{"rtt_ms": r.ClientRTTMs, "rttvar_ms": r.ClientRTTVar, "retrans": r.ClientRetrans,
			"lost": r.ClientLost, "unacked": r.ClientUnacked, "cwnd": r.ClientCwnd, "segs_out": r.ClientSegsOut, "segs_in": r.ClientSegsIn}
	}
	return json.Marshal(m)
}

// RegisterOpsRoutes mounts the agent's view: GET /api/v1/ops/legs?last=24h
// [&who=account].
func RegisterOpsRoutes(mux *http.ServeMux, q Querier,
	resolve func(ctx context.Context, ref string) (string, error), name func(ctx context.Context) (func(subject string) string, error),
	gate func(action string, h http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/ops/legs", gate("", func(w http.ResponseWriter, r *http.Request) {
		v := r.URL.Query()
		span, err := parseSpan(v.Get("last"), 24*time.Hour)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "last: 1h, 24h, 7d"})
			return
		}
		subject := ""
		if who := v.Get("who"); who != "" {
			if subject, err = resolve(r.Context(), who); err != nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
				return
			}
		}
		since := time.Now().Add(-span)
		obs, err := Read(r.Context(), q, since, subject)
		if err != nil {
			applog.Error(r.Context(), "testerlegs", "read failed", applog.F("err", err.Error()))
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		if names, err := name(r.Context()); err == nil {
			for i := range obs {
				obs[i].Who = names(obs[i].Subject)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"since": since.UTC(), "observations": obs, "guide": Guide})
	}))
}

// Guide is what an answer's numbers mean.
const Guide = "Участки пути тестера через каскад (hikkari-control internal/testerlegs). Одно наблюдение = один поток (stream, TCP клиента) или поток датаграмм (flow, UDP), помеченный выходом по ключу тестера; оба конца пишут его под одним id при метке (mark) и на конце (end). entry.client.*: TCP клиента до входа глазами ядра входа (участок клиент → вход): rtt_ms, rttvar_ms, retrans (переотправки всего), lost, cwnd, segs_out/segs_in. Участок вход → выход (магистраль) у каждого конца: chunks отправлено кусков, resent из них переотправлено после смерти члена пула, carriers сколько членов пула везли, deaths сколько членов умерло под потоком, max_wait_ms самое долгое ожидание подтверждения куска. up у входа = байты от клиента, у выхода = байты от релея; down наоборот: up входа должно равняться down выхода. dg_out/dg_in датаграммы туда и обратно, dg_copy отброшенные копии: dg_out входа минус dg_in выхода = потери магистрали вверх. end: fin (закрыт обеими сторонами), local (свой конец оборвался), peer (другой конец сбросил), epoch (выход перезапустился), session, idle, open_fail. Что сессия видела дальше выхода: инструмент hikkari, side relay, path entry, тот же аккаунт."

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

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
