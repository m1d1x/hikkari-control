package cascadewatch

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

// The receivers' egresses (H4 of hikkari-client
// plans/2026-11-16-hidden-border-receiver-and-egress.md). Every receiver
// behind an egress runs its own watch (hikkari-egress-watch, the tunnel
// program in internal/entry/egress.go), which pings each egress through the
// tunnel and the internet through the one that carries, once a second, and
// moves to the spare when the one that carries dies. Its lines, by box names:
//
//	egress: minute from=<unix> <box> active tun=N/M rtt_ms=R net=N/M; <box> standby tun=N/M rtt_ms=R
//	egress: moved from <box> to <box>: <why>
//	egress: <box> red, not moved: <why>; <why not>
//	egress: <box> carries now: by the hub
//
// The watch reads them every minute beside the entries' journals. A minute
// is a row of the egress layer per egress (entry_box the receiver, exit_box
// and target the egress, vantage its role); a move, a refusal and a move by
// the hub are events (kind egress_moved, egress_stuck, egress_by_hub). A move
// by the watch is an alert too: people's address on the internet changed.

// LayerEgress is a minute of a receiver's watch of one egress (migration 074).
const LayerEgress = "egress"

// egressWatchUnit is the receivers' watch (internal/entry egress.go).
const egressWatchUnit = "hikkari-egress-watch"

var (
	reEgressMinute = regexp.MustCompile(`^egress: minute from=(\d+) (.+)$`)
	reEgressPart   = regexp.MustCompile(`^(\S+) (active|standby) tun=(\d+)/(\d+) rtt_ms=(-?[0-9.]+)(?: net=(\d+)/(\d+))?$`)
	reEgressMoved  = regexp.MustCompile(`^egress: moved from (\S+) to (\S+): (.+)$`)
	reEgressStuck  = regexp.MustCompile(`^egress: (\S+) red, not moved: (.+)$`)
	reEgressByHub  = regexp.MustCompile(`^egress: (\S+) carries now: by the hub$`)
)

// egressFleet is the fleet's receivers behind an egress (entry.Service).
type egressFleet interface {
	EgressReceivers(ctx context.Context) ([]entry.EgressReceiver, error)
}

// classifyEgress turns a line of a receiver's watch into its rows; false for
// any other line.
func classifyEgress(l journalLine, rx string) ([]Run, bool) {
	if m := reEgressMinute.FindStringSubmatch(l.Msg); m != nil {
		from, _ := strconv.ParseInt(m[1], 10, 64)
		var runs []Run
		for _, part := range strings.Split(m[2], "; ") {
			p := reEgressPart.FindStringSubmatch(part)
			if p == nil {
				continue
			}
			ok, _ := strconv.Atoi(p[3])
			n, _ := strconv.Atoi(p[4])
			rtt, _ := strconv.ParseFloat(p[5], 64)
			data := map[string]any{"from": from, "role": p[2], "tun_ok": ok, "tun_n": n}
			if rtt >= 0 {
				data["rtt_ms"] = rtt
			}
			good := n > 0 && ok == n
			if p[6] != "" {
				netOK, _ := strconv.Atoi(p[6])
				netN, _ := strconv.Atoi(p[7])
				data["net_ok"], data["net_n"] = netOK, netN
				good = good && netOK == netN
			}
			runs = append(runs, Run{TS: time.Unix(from, 0).UTC(), Entry: rx, Exit: p[1], Layer: LayerEgress,
				Vantage: p[2], Target: p[1], OK: boolp(good), Data: data})
		}
		return runs, len(runs) > 0
	}
	if m := reEgressMoved.FindStringSubmatch(l.Msg); m != nil {
		return []Run{{TS: l.TS, Entry: rx, Exit: m[2], Layer: LayerEvent, Vantage: "receiver",
			Data: map[string]any{"kind": "egress_moved", "from": m[1], "to": m[2], "why": clip(m[3])}}}, true
	}
	if m := reEgressStuck.FindStringSubmatch(l.Msg); m != nil {
		return []Run{{TS: l.TS, Entry: rx, Exit: m[1], Layer: LayerEvent, Vantage: "receiver",
			Data: map[string]any{"kind": "egress_stuck", "egress": m[1], "why": clip(m[2])}}}, true
	}
	if m := reEgressByHub.FindStringSubmatch(l.Msg); m != nil {
		return []Run{{TS: l.TS, Entry: rx, Exit: m[1], Layer: LayerEvent, Vantage: "receiver",
			Data: map[string]any{"kind": "egress_by_hub", "to": m[1]}}}, true
	}
	return nil, false
}

// collectEgresses reads every receiver's watch since the last line read;
// quiet receivers (border points) are read without alerts.
func (w *Watch) collectEgresses(ctx context.Context, quiet map[string]bool) {
	f, ok := w.fleet.(egressFleet)
	if !ok {
		return
	}
	rxs, err := f.EgressReceivers(ctx)
	if err != nil {
		applog.Error(ctx, "cascade", "egress receivers", applog.F("err", err.Error()))
		return
	}
	for _, rx := range rxs {
		if err := w.collectEgress(ctx, rx, quiet[rx.Box]); err != nil {
			applog.Error(ctx, "cascade", "receiver journal", applog.F("receiver", rx.Box), applog.F("err", err.Error()))
		}
	}
}

func (w *Watch) collectEgress(ctx context.Context, rx entry.EgressReceiver, quiet bool) error {
	key := "egress:" + rx.Box
	w.mu.Lock()
	since := w.cursor[key]
	w.mu.Unlock()
	if since.IsZero() {
		read, err := w.store.journalCursor(ctx, rx.Box)
		if err != nil {
			return err
		}
		since = w.now().Add(-15 * time.Minute)
		if read.After(since) {
			since = read
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	r, err := w.boxes.WatchRemote(ctx, rx.Box)
	if err != nil {
		return err
	}
	defer r.Close()
	out, err := r.Run(ctx, fmt.Sprintf("journalctl -u %s -o short-unix --no-pager --since @%d", egressWatchUnit, since.Unix()), nil)
	if err != nil {
		return fmt.Errorf("journalctl: %w", err)
	}
	var runs []Run
	newest := since
	for _, l := range parseJournal(out) {
		if !l.TS.After(since) {
			continue
		}
		if l.TS.After(newest) {
			newest = l.TS
		}
		got, ok := classifyEgress(l, rx.Box)
		if !ok {
			continue
		}
		for _, run := range got {
			if quiet {
				continue
			}
			if run.Data["kind"] == "egress_moved" {
				w.alert(ctx, entry.TrunkPair{Entry: rx.Box, Exit: run.Exit}, "cascade.egress_moved",
					fmt.Sprintf("приёмник %s ушёл с выхода %s на %s в %s: %s; адрес людей в интернете сменился",
						rx.Box, run.Data["from"], run.Data["to"], run.TS.Format("15:04:05"), run.Data["why"]))
			}
			if run.Data["kind"] == "egress_stuck" {
				w.alert(ctx, entry.TrunkPair{Entry: rx.Box, Exit: run.Exit}, "cascade.egress_stuck",
					fmt.Sprintf("приёмник %s: выход %s красный, уйти некуда: %s", rx.Box, run.Exit, run.Data["why"]))
			}
		}
		runs = append(runs, got...)
	}
	if err := w.store.InsertJournal(ctx, rx.Box, newest, runs); err != nil {
		return err
	}
	w.mu.Lock()
	w.cursor[key] = newest
	w.mu.Unlock()
	return nil
}

// The egresses on the admin screen: for every receiver and egress, how many
// seconds of a bucket the egress did not answer through the tunnel, how many
// the internet did not answer through it while it carried, its round trip,
// and who carried; moves and refusals as marks. Only charts.
const (
	ChartEgressTun   = "egress_tun"   // seconds without an answer through the tunnel
	ChartEgressNet   = "egress_net"   // seconds the internet was silent through the egress that carried
	ChartEgressRTT   = "egress_rtt"   // round trip through the tunnel to the egress, median of the minutes
	ChartEgressCarry = "egress_carry" // minutes the egress carried the receiver
)

// BuildEgressChart reads the window of the receivers' watches.
func (s *Store) BuildEgressChart(ctx context.Context, window string, now time.Time) (WireChart, error) {
	window, span, step, err := chartSpan(window)
	if err != nil {
		return WireChart{}, err
	}
	until := now.Truncate(step).Add(step)
	since := until.Add(-span)
	c := WireChart{Window: window, BucketSeconds: int64(step / time.Second), Marks: []ChartMark{}}
	index := map[int64]int{}
	for b := since; b.Before(until); b = b.Add(step) {
		index[b.Unix()] = len(c.Buckets)
		c.Buckets = append(c.Buckets, b.UTC().Format(time.RFC3339))
	}
	lines := map[string]*ChartLine{}
	var order []string
	put := func(group, key string, bucket int64, v *float64) {
		i, ok := index[bucket]
		if !ok || v == nil {
			return
		}
		id := group + "|" + key
		l := lines[id]
		if l == nil {
			l = &ChartLine{Key: key, Group: group, Values: make([]*float64, len(c.Buckets))}
			lines[id] = l
			order = append(order, id)
		}
		x := *v
		l.Values[i] = &x
	}
	rows, err := s.pool.Query(ctx, `
		SELECT entry_box || ' → ' || target, (floor(extract(epoch FROM ts) / $3) * $3)::bigint AS b,
		       sum((data->>'tun_n')::float8 - (data->>'tun_ok')::float8),
		       sum((data->>'net_n')::float8 - (data->>'net_ok')::float8),
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY (data->>'rtt_ms')::float8),
		       count(*) FILTER (WHERE vantage = 'active')::float8
		FROM trunk_runs
		WHERE layer = 'egress' AND ts >= $1 AND ts < $2
		GROUP BY 1, 2 ORDER BY 1, 2`, since, until, int64(step/time.Second))
	if err != nil {
		return WireChart{}, fmt.Errorf("egresses: %w", err)
	}
	for rows.Next() {
		var key string
		var b int64
		var tun, net, rtt *float64
		var carry float64
		if err := rows.Scan(&key, &b, &tun, &net, &rtt, &carry); err != nil {
			rows.Close()
			return WireChart{}, err
		}
		put(ChartEgressTun, key, b, tun)
		put(ChartEgressNet, key, b, net)
		put(ChartEgressRTT, key, b, rtt)
		put(ChartEgressCarry, key, b, &carry)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return WireChart{}, err
	}
	rows, err = s.pool.Query(ctx, `
		SELECT ts, entry_box, data FROM (
			SELECT ts, id, entry_box, data FROM trunk_runs
			WHERE layer = 'event' AND data->>'kind' IN ('egress_moved', 'egress_stuck', 'egress_by_hub') AND ts >= $1 AND ts < $2
			ORDER BY ts DESC, id DESC LIMIT $3) m
		ORDER BY ts, id`, since, until, maxMarks)
	if err != nil {
		return WireChart{}, fmt.Errorf("egress moves: %w", err)
	}
	for rows.Next() {
		var ts time.Time
		var rx string
		var data map[string]any
		if err := rows.Scan(&ts, &rx, &data); err != nil {
			rows.Close()
			return WireChart{}, err
		}
		c.Marks = append(c.Marks, egressMark(ts, rx, data))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return WireChart{}, err
	}
	for _, id := range order {
		c.Lines = append(c.Lines, *lines[id])
	}
	return c, nil
}

// egressMark says a receiver's move as the screen's tooltip does.
func egressMark(ts time.Time, rx string, data map[string]any) ChartMark {
	str := func(k string) string { v, _ := data[k].(string); return v }
	kind := str("kind")
	var label, why string
	switch kind {
	case "egress_moved":
		label, why = fmt.Sprintf("%s: выход %s умер, на %s (%s)", rx, str("from"), str("to"), str("why")), "red"
	case "egress_stuck":
		label, why = fmt.Sprintf("%s: выход %s красный, не ушёл: %s", rx, str("egress"), str("why")), "stuck"
	default:
		label, why = fmt.Sprintf("%s: на %s руками с hub", rx, str("to")), "hand"
	}
	return ChartMark{At: ts.UTC().Format(time.RFC3339), Label: label, Class: "egress", Why: why}
}
