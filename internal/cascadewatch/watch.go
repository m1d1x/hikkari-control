// Package cascadewatch watches the Russian cascade (hikkari-client
// plans/2026-11-05-ru-cascade.md) from the hub, layer by layer, so that when
// it fails the dataset says who failed: the box, the path between the data
// centres, the trunk, or the protocol.
//
//	client ─► entry (RU) ═ trunk, 32 members ═► exit relay (NL) ─► internet
//
// Every minute it reads the entry's journal over SSH (the hub's own key, as
// every command on a box): the minute line of the pool's counters and the
// members' round trips, and a row per member that died, per epoch of the
// exit, per start. Every round (10 minutes), with one round time for all:
//
//   - vps: the entry and the exit box, read by nekko-cascade-probe (CPU steal,
//     load, memory, interface drops, conntrack, sockets);
//   - path: bare TCP from 12 source ports and ICMP from the entry to the
//     exit's relay port, and the same from the control vantage abroad;
//   - trunk: chunks from the exit's provoke receiver, single and pool, straight
//     across the border and through the entry's loopback port, and straight
//     from the control vantage;
//   - protocol: a cascade task of the stand on every observation point (the
//     client core on the exit's Hikkari relay, straight and through the
//     entry: diag:load's load and a 30 s call), queued last, so it does not
//     run beside the chunks.
//
// A round's verdict is written when its layers are in, or 15 minutes on:
// the first layer from the bottom that read red. What must be seen at once
// goes to the client errors as cascade.*: the entry silent, its pool thin,
// members stalling, the protocol red two rounds running, every red round.
// The watch only looks: it heals nothing.
package cascadewatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/db"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/stand"
	"github.com/m1d1x/hikkari-control/internal/subscription"
	"github.com/m1d1x/hikkari-control/internal/telemetry"
	"github.com/m1d1x/hikkari-control/internal/testerlegs"
)

// Account is the hub's own account whose key the protocol layer dials with.
// It holds the whole fleet; the operator makes it once (hub subscriber-add,
// then subscriber-cascade, which also sets all_relays).
const Account = "cascade-watch"

// ProvokePort is the exit's provoke receiver (edge/provoke.go), whose chunks
// the trunk layer fetches.
const ProvokePort = 2053

const (
	Every        = 10 * time.Minute
	verdictAfter = 15 * time.Minute
	silentAfter  = 3 * time.Minute
	alertEvery   = 30 * time.Minute
	stallWindow  = 10 * time.Minute
	stallAlert   = 5
	// callAlertMs: a pause of the trunk's call this long is a call broken
	// for whoever is on one, not a frame lost.
	callAlertMs = 500
)

// callOK: a minute of the trunk's call in which no call would have heard a
// word go missing.
func callOK(data map[string]any) bool { return num(data, "gaps150") == 0 }

// Fleet is the fleet as the watch needs it (entry.Service).
type Fleet interface {
	TrunkPairs(ctx context.Context) ([]entry.TrunkPair, error)
	VantageBox(ctx context.Context, slot string) (string, error)
	RelayStatuses(ctx context.Context, box string) (map[string]string, error)
}

// Boxes opens a box and stages the probe on it (entry.Provisioner).
type Boxes interface {
	WatchRemote(ctx context.Context, box string) (entry.Remote, error)
	StageCascadeProbe(ctx context.Context, r entry.Remote) (string, error)
}

// Links renders the protocol layer's links (subscription.Service).
type Links interface {
	CascadeProbeLinks(ctx context.Context, account, entryBox, exitBox string, port int) ([]string, error)
}

// Queue queues the protocol layer's tasks (stand.Scheduler).
type Queue interface {
	QueueCascade(ctx context.Context, entryBox string, round time.Time, slots []string) (int, error)
}

// Watch is the watcher.
type Watch struct {
	pool  db.Pool
	store *Store
	fleet Fleet
	boxes Boxes
	links Links
	queue Queue
	errs  telemetry.Sink
	legs  *testerlegs.Recorder
	now   func() time.Time

	mu      sync.Mutex
	cursor  map[string]time.Time // entry box → last journal line read
	busy    map[string]bool      // entry box → a round running
	last    map[string]time.Time // entry box → last round started
	alerted map[string]time.Time // code|entry → last alert
	started time.Time
	rounds  sync.WaitGroup
}

// SetTesterLegs files the entries' observed lines (internal/testerlegs);
// nil leaves them in the journal.
func (w *Watch) SetTesterLegs(r *testerlegs.Recorder) { w.legs = r }

func New(pool db.Pool, fleet Fleet, boxes Boxes, links Links, queue Queue, errs telemetry.Sink) *Watch {
	return &Watch{
		pool: pool, store: NewStore(pool), fleet: fleet, boxes: boxes, links: links, queue: queue, errs: errs,
		now:    time.Now,
		cursor: map[string]time.Time{}, busy: map[string]bool{}, last: map[string]time.Time{}, alerted: map[string]time.Time{},
	}
}

// Wait waits for the rounds running now.
func (w *Watch) Wait() { w.rounds.Wait() }

// Store is the watch's dataset.
func (w *Watch) Store() *Store { return w.store }

// Run ticks every minute until ctx ends.
func (w *Watch) Run(ctx context.Context) {
	w.started = w.now()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		w.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick is one minute of the watch.
func (w *Watch) Tick(ctx context.Context) {
	pairs, err := w.fleet.TrunkPairs(ctx)
	if err != nil {
		applog.Error(ctx, "cascade", "trunk pairs", applog.F("err", err.Error()))
		return
	}
	for _, p := range pairs {
		if err := w.collect(ctx, p); err != nil {
			applog.Error(ctx, "cascade", "entry journal", applog.F("entry", p.Entry), applog.F("err", err.Error()))
		}
		w.alertEntry(ctx, p, err)
		if err := w.judgePending(ctx, p); err != nil {
			applog.Error(ctx, "cascade", "verdicts", applog.F("entry", p.Entry), applog.F("err", err.Error()))
		}
		w.startRound(ctx, p)
	}
}

// collect reads the entry's journal since the last line read.
func (w *Watch) collect(ctx context.Context, p entry.TrunkPair) error {
	w.mu.Lock()
	since := w.cursor[p.Entry]
	w.mu.Unlock()
	if since.IsZero() {
		last, err := w.store.last(ctx, p.Entry, LayerEntry, "")
		if err != nil {
			return err
		}
		since = w.now().Add(-15 * time.Minute)
		if last != nil && last.TS.After(since) {
			since = last.TS
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	r, err := w.boxes.WatchRemote(ctx, p.Entry)
	if err != nil {
		return err
	}
	defer r.Close()
	out, err := r.Run(ctx, fmt.Sprintf("journalctl -u nekko-trunk-entry -o short-unix --no-pager --since @%d", since.Unix()), nil)
	if err != nil {
		return fmt.Errorf("journalctl: %w", err)
	}
	var runs []Run
	var legs []testerlegs.Row
	newest := since
	for _, l := range parseJournal(out) {
		if !l.TS.After(since) {
			continue
		}
		if l.TS.After(newest) {
			newest = l.TS
		}
		if testerlegs.IsEntryLine(l.Msg) {
			if row, err := testerlegs.ParseEntryLine(p.Entry, l.TS, l.Msg); err == nil {
				legs = append(legs, row)
			}
			continue
		}
		layer, data, ok := classify(l)
		if !ok {
			continue
		}
		run := Run{TS: l.TS, Entry: p.Entry, Exit: p.Exit, Layer: layer, Vantage: "entry", Data: data}
		switch layer {
		case LayerEntry:
			run.OK = boolp(num(data, "members") >= minMembers)
		case LayerCall:
			// A minute of the trunk's call: its start is its time, so the
			// rows of both calls and the entry's minute line up.
			run.TS = time.Unix(int64(num(data, "from")), 0).UTC()
			run.Target = fmt.Sprintf("copies%d", int(num(data, "copies")))
			run.OK = boolp(callOK(data))
			if num(data, "gap_max_ms") >= callAlertMs {
				w.alert(ctx, p, "cascade.call_gap", fmt.Sprintf("звонок магистрали %s → %s (%s): тишина %d мс в минуту %s, всего %d мс, потеряно %d из %d",
					p.Entry, p.Exit, run.Target, int(num(data, "gap_max_ms")), run.TS.Format("15:04"), int(num(data, "silent_ms")), int(num(data, "lost")), int(num(data, "sent"))))
			}
		}
		runs = append(runs, run)
	}
	if err := w.store.Insert(ctx, runs...); err != nil {
		return err
	}
	if w.legs != nil {
		if err := w.legs.RecordEntry(ctx, legs); err != nil {
			return err
		}
	}
	w.mu.Lock()
	w.cursor[p.Entry] = newest
	w.mu.Unlock()
	return nil
}

// alertEntry says what of the entry must be seen now: silence, a thin pool,
// members stalling.
func (w *Watch) alertEntry(ctx context.Context, p entry.TrunkPair, collectErr error) {
	now := w.now()
	if now.Sub(w.started) < silentAfter {
		return // the journal of a hub just up is still being read back
	}
	last, err := w.store.last(ctx, p.Entry, LayerEntry, "")
	if err != nil {
		return
	}
	if last == nil || now.Sub(last.TS) > silentAfter {
		since := "никогда"
		if last != nil {
			since = last.TS.Format(time.RFC3339)
		}
		msg := fmt.Sprintf("вход %s → %s молчит: последняя строка stats %s", p.Entry, p.Exit, since)
		if collectErr != nil {
			msg += "; журнал не прочитан: " + clip(collectErr.Error())
		}
		w.alert(ctx, p, "cascade.entry_silent", msg)
		return
	}
	if m := num(last.Data, "members"); m < minMembers {
		w.alert(ctx, p, "cascade.members_low", fmt.Sprintf("вход %s → %s: членов пула %d из %d", p.Entry, p.Exit, int(m), p.Members))
	}
	minutes, err := w.store.Rows(ctx, Query{Since: now.Add(-stallWindow - time.Minute), Entry: p.Entry, Layer: LayerEntry})
	if err != nil || len(minutes) < 2 {
		return
	}
	first, lastRow := minutes[0], minutes[len(minutes)-1]
	if d := num(lastRow.Data, "stalled") - num(first.Data, "stalled"); d >= stallAlert && num(lastRow.Data, "joined") >= num(first.Data, "joined") {
		w.alert(ctx, p, "cascade.stalls", fmt.Sprintf("вход %s → %s: %d членов пула встали за %s (повторов %d, портов выведено %d)",
			p.Entry, p.Exit, int(d), lastRow.TS.Sub(first.TS).Round(time.Minute),
			int(num(lastRow.Data, "resent")-num(first.Data, "resent")), int(num(lastRow.Data, "port_kills")-num(first.Data, "port_kills"))))
	}
}

// alert writes one row to the client errors, at most once per alertEvery per
// code and entry.
func (w *Watch) alert(ctx context.Context, p entry.TrunkPair, code, msg string) {
	key := code + "|" + p.Entry
	now := w.now()
	w.mu.Lock()
	if at, ok := w.alerted[key]; ok && now.Sub(at) < alertEvery {
		w.mu.Unlock()
		return
	}
	w.alerted[key] = now
	w.mu.Unlock()
	applog.Error(ctx, "cascade", msg, applog.F("code", code), applog.F("entry", p.Entry))
	if w.errs == nil {
		return
	}
	row := telemetry.ErrorRow{
		TS: now.UTC(), Install: "cascade:" + p.Entry,
		App:  telemetry.App{Version: "hub", Channel: "hub", OS: "linux"},
		Code: code, Component: "cascade", FP: alertFP(code, p), Msg: clip(msg), Count: 1,
	}
	if err := w.errs.InsertTelemetry(ctx, nil, []telemetry.ErrorRow{row}); err != nil {
		applog.Error(ctx, "cascade", "alert not written", applog.F("err", err.Error()))
	}
}

// alertFP groups an alert by code and pair: 16 hex, the errors table's
// fingerprint width.
func alertFP(code string, p entry.TrunkPair) string {
	sum := sha256.Sum256([]byte(code + "\x00" + p.Entry + "\x00" + p.Exit))
	return hex.EncodeToString(sum[:8])
}

// startRound starts the pair's round when one is due and none runs.
func (w *Watch) startRound(ctx context.Context, p entry.TrunkPair) {
	round := roundOf(w.now(), Every)
	w.mu.Lock()
	if w.busy[p.Entry] {
		w.mu.Unlock()
		return
	}
	last := w.last[p.Entry]
	w.mu.Unlock()
	if last.IsZero() {
		var err error
		if last, err = w.store.lastRound(ctx, p.Entry); err != nil {
			return
		}
	}
	if !round.After(last) {
		return
	}
	w.mu.Lock()
	w.busy[p.Entry], w.last[p.Entry] = true, round
	w.mu.Unlock()
	w.rounds.Add(1)
	go func() {
		defer w.rounds.Done()
		defer func() { w.mu.Lock(); w.busy[p.Entry] = false; w.mu.Unlock() }()
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Minute)
		defer cancel()
		if err := w.Round(rctx, p, round); err != nil {
			applog.Error(ctx, "cascade", "round", applog.F("entry", p.Entry), applog.F("err", err.Error()))
		}
	}()
}

// box is an open box with the probe on it.
type box struct {
	name, vantage string
	r             entry.Remote
	probe         string
	err           error
}

func (w *Watch) open(ctx context.Context, name, vantage string) *box {
	b := &box{name: name, vantage: vantage}
	if name == "" {
		b.err = errors.New("нет бокса")
		return b
	}
	if b.r, b.err = w.boxes.WatchRemote(ctx, name); b.err != nil {
		return b
	}
	b.probe, b.err = w.boxes.StageCascadeProbe(ctx, b.r)
	return b
}

func (b *box) close() {
	if b.r != nil {
		b.r.Close()
	}
}

// measure runs the probe with args and reads its JSON line.
func (b *box) measure(ctx context.Context, args ...string) (map[string]any, error) {
	if b.err != nil {
		return nil, b.err
	}
	out, err := b.r.Run(ctx, b.probe+" "+strings.Join(args, " "), nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", strings.Join(args[:1], " "), err)
	}
	var d map[string]any
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &d); err != nil {
		return nil, fmt.Errorf("%s: unreadable answer", args[0])
	}
	return d, nil
}

// Round measures the pair's layers for round.
func (w *Watch) Round(ctx context.Context, p entry.TrunkPair, round time.Time) error {
	row := func(layer, vantage, target string, d map[string]any, ok bool, why string) Run {
		if why != "" {
			d["why"] = why
		}
		rt := round
		return Run{TS: w.now().UTC(), Round: &rt, Entry: p.Entry, Exit: p.Exit, Layer: layer, Vantage: vantage, Target: target, OK: boolp(ok), Data: d}
	}
	failed := func(layer, vantage, target string, err error) Run {
		return row(layer, vantage, target, map[string]any{"error": clip(err.Error())}, false, "не измерено: "+clip(err.Error()))
	}
	controlSlot, err := w.controlSlot(ctx)
	if err != nil {
		return err
	}
	controlBox, err := w.fleet.VantageBox(ctx, controlSlot)
	if err != nil {
		return err
	}
	ent := w.open(ctx, p.Entry, "entry")
	defer ent.close()
	ext := w.open(ctx, p.Exit, "exit")
	defer ext.close()
	ctl := w.open(ctx, controlBox, controlSlot)
	defer ctl.close()

	// The boxes.
	for _, b := range []*box{ent, ext} {
		d, err := b.measure(ctx, "vps")
		if err != nil {
			// A box the hub cannot reach is a box that is down, as far as
			// the cascade goes.
			if insErr := w.store.Insert(ctx, failed(LayerVPS, b.vantage, b.name, err)); insErr != nil {
				return insErr
			}
			continue
		}
		var prevData map[string]any
		if prev, err := w.store.last(ctx, p.Entry, LayerVPS, b.vantage); err == nil && prev != nil && prev.Data["error"] == nil {
			prevData = prev.Data
		}
		ok, why := judgeVPS(d, prevData)
		if b == ext {
			// The exit's relays as the pool holds them: a canary of the fleet's
			// rollout swaps the relay's build and restarts it under the round.
			if st, err := w.fleet.RelayStatuses(ctx, p.Exit); err == nil {
				d["relays"] = st
			}
		}
		if err := w.store.Insert(ctx, row(LayerVPS, b.vantage, b.name, d, ok, why)); err != nil {
			return err
		}
	}

	// The path, from the entry and from the control vantage.
	relay := fmt.Sprintf("%s:%d", p.ExitAddress, p.RelayPort)
	for _, b := range []*box{ent, ctl} {
		d, err := b.measure(ctx, "path", relay, "12")
		if err != nil {
			if err := w.store.Insert(ctx, failed(LayerPath, b.vantage, "exit", err)); err != nil {
				return err
			}
			continue
		}
		ok, why := judgePath(d)
		if err := w.store.Insert(ctx, row(LayerPath, b.vantage, "exit", d, ok, why)); err != nil {
			return err
		}
	}

	// The trunk: chunks straight and through the entry.
	direct := fmt.Sprintf("%s:%d", p.ExitAddress, ProvokePort)
	listen := p.ProvokeListen(ProvokePort)
	type run struct {
		b            *box
		mode, target string
		addr         string
	}
	runs := []run{{ent, "single", "direct", direct}}
	if listen > 0 {
		runs = append(runs, run{ent, "single", "entry", fmt.Sprintf("127.0.0.1:%d", listen)})
	}
	runs = append(runs, run{ent, "pool", "direct", direct})
	if listen > 0 {
		runs = append(runs, run{ent, "pool", "entry", fmt.Sprintf("127.0.0.1:%d", listen)})
	}
	runs = append(runs, run{ctl, "single", "direct", direct}, run{ctl, "pool", "direct", direct})
	entBusy := ent.busyWithTrunkprobe(ctx)
	for _, t := range runs {
		if t.b == ent && entBusy {
			// Another experiment's chunks on the same box would be measured
			// against each other: the run is skipped, not read as red.
			r := row(LayerTrunk, t.b.vantage, t.target, map[string]any{"mode": t.mode, "busy": true}, true, "")
			r.OK = nil
			if err := w.store.Insert(ctx, r); err != nil {
				return err
			}
			continue
		}
		d, err := t.b.measure(ctx, "trunk", t.mode, t.addr, "48", strconv.Itoa(256<<10))
		if err != nil {
			if err := w.store.Insert(ctx, failed(LayerTrunk, t.b.vantage, t.target, err)); err != nil {
				return err
			}
			continue
		}
		delete(d, "target") // an address, which the row names by target
		ok, why := judgeTrunk(d)
		if err := w.store.Insert(ctx, row(LayerTrunk, t.b.vantage, t.target, d, ok, why)); err != nil {
			return err
		}
	}

	// The protocol: the points' cascade tasks, last.
	slots, err := w.protocolSlots(ctx)
	if err != nil {
		return err
	}
	if _, err := w.queue.QueueCascade(ctx, p.Entry, round, slots); err != nil {
		return err
	}
	return nil
}

// busyWithTrunkprobe: another experiment's chunk runs on the box now.
func (b *box) busyWithTrunkprobe(ctx context.Context) bool {
	if b.err != nil {
		return false
	}
	out, _ := b.r.Run(ctx, "pgrep -x trunkprobe >/dev/null && echo busy || true", nil)
	return strings.TrimSpace(out) == "busy"
}

// controlSlot is the stand's control vantage abroad (stand_settings).
func (w *Watch) controlSlot(ctx context.Context) (string, error) {
	var slots []string
	if err := w.pool.QueryRow(ctx, `SELECT control_slots FROM stand_settings WHERE id`).Scan(&slots); err != nil {
		return "", fmt.Errorf("stand control slots: %w", err)
	}
	if len(slots) == 0 {
		return "", errors.New("у стенда нет контрольной точки (stand_settings.control_slots)")
	}
	return slots[0], nil
}

// protocolSlots are the active points in Russia and the control vantage.
func (w *Watch) protocolSlots(ctx context.Context) ([]string, error) {
	rows, err := w.pool.Query(ctx, `
		SELECT slot FROM entry_vantages v WHERE status = 'active'
		  AND (in_russia OR slot = ANY (coalesce((SELECT control_slots FROM stand_settings WHERE id), '{}'::text[])))
		ORDER BY slot`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (w *Watch) ruSlots(ctx context.Context) (map[string]bool, error) {
	rows, err := w.pool.Query(ctx, `SELECT slot FROM entry_vantages WHERE status = 'active' AND in_russia`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out[s] = true
	}
	return out, rows.Err()
}

// CascadeLinks are the protocol layer's links for a task of entryBox: the
// exit's Hikkari relay straight and through the entry's port that reaches it.
func (w *Watch) CascadeLinks(ctx context.Context, entryBox string) ([]string, error) {
	p, err := w.pair(ctx, entryBox)
	if err != nil {
		return nil, err
	}
	port := 0
	for l, to := range p.Forward {
		if to == p.RelayPort && (port == 0 || l < port) {
			port = l
		}
	}
	if port == 0 {
		return nil, fmt.Errorf("вход %s не ведёт к порту релея %d", entryBox, p.RelayPort)
	}
	return w.links.CascadeProbeLinks(ctx, Account, p.Entry, p.Exit, port)
}

func (w *Watch) pair(ctx context.Context, entryBox string) (entry.TrunkPair, error) {
	pairs, err := w.fleet.TrunkPairs(ctx)
	if err != nil {
		return entry.TrunkPair{}, err
	}
	for _, p := range pairs {
		if p.Entry == entryBox {
			return p, nil
		}
	}
	return entry.TrunkPair{}, fmt.Errorf("входа %s нет в trunk_entries", entryBox)
}

// CascadeFinished writes a point's cascade task as the round's protocol rows:
// one per profile, its load and its call.
func (w *Watch) CascadeFinished(ctx context.Context, slot, entryBox string, round time.Time, res stand.Result) error {
	p, err := w.pair(ctx, entryBox)
	if err != nil {
		return err
	}
	byProfile := map[string]map[string]any{}
	get := func(profile string) map[string]any {
		if byProfile[profile] == nil {
			byProfile[profile] = map[string]any{"profile": profile, "core": res.Core}
		}
		return byProfile[profile]
	}
	for _, r := range res.Rows {
		d := get(r.Profile)
		switch {
		case r.Knob == stand.Baseline && r.Value != stand.ValueNoProtocol:
			d["addr"] = r.Value
		case r.Knob == stand.Baseline:
			d["base_dl"], d["base_dl_end"], d["base_burst"] = r.Dl, r.DlEnd, r.Burst
		default:
			d["dl"], d["dl_total"], d["dl_end"] = r.Dl, int64(stand.Bytes), r.DlEnd
			d["burst"], d["burst_total"], d["burst_timeout"] = r.Burst, r.BurstTotal, r.BurstTimeout
			d["after"], d["after_total"] = r.After, stand.After
			if r.LoadError {
				d["load_error"] = true
			}
		}
	}
	for _, v := range res.Voice {
		d := get(v.Profile)
		d["voice_sent"], d["voice_got"], d["voice_loss_run"] = v.Sent, v.Got, v.LossRun
		d["gaps60"], d["gaps150"], d["gap_max_ms"] = v.Gaps60, v.Gaps150, v.GapMax
		d["voice_rtt_p50_ms"], d["voice_jitter_ms"] = v.RTTp50, v.Jitter
		if v.Error != "" {
			d["voice_error"] = v.Error
		}
	}
	rt := round
	var runs []Run
	for profile, d := range byProfile {
		target := ""
		switch {
		case strings.HasPrefix(profile, subscription.CascadeProbeLabel(subscription.CascadeProbeDirect, "")):
			target = "direct"
		case strings.HasPrefix(profile, subscription.CascadeProbeLabel(subscription.CascadeProbeEntry, "")):
			target = "entry"
		default:
			continue
		}
		if res.Error != "" {
			d["task_error"] = clip(res.Error)
		}
		ok, why := judgeProtocol(d)
		if why != "" {
			d["why"] = why
		}
		runs = append(runs, Run{TS: w.now().UTC(), Round: &rt, Entry: p.Entry, Exit: p.Exit, Layer: LayerProtocol,
			Vantage: slot, Target: target, OK: boolp(ok), Data: d})
	}
	if len(runs) == 0 {
		why := "задача без строк"
		if res.Error != "" {
			why = clip(res.Error)
		}
		runs = append(runs, Run{TS: w.now().UTC(), Round: &rt, Entry: p.Entry, Exit: p.Exit, Layer: LayerProtocol,
			Vantage: slot, Target: "entry", OK: boolp(false), Data: map[string]any{"why": "не измерено: " + why}})
	}
	return w.store.Insert(ctx, runs...)
}

// judgePending writes the verdicts of the pair's rounds that are complete or
// old enough, and alerts on them.
func (w *Watch) judgePending(ctx context.Context, p entry.TrunkPair) error {
	now := w.now()
	rounds, err := w.store.pendingRounds(ctx, p.Entry, now.Add(-3*time.Hour))
	if err != nil {
		return err
	}
	if len(rounds) == 0 {
		return nil
	}
	ru, err := w.ruSlots(ctx)
	if err != nil {
		return err
	}
	slots, err := w.protocolSlots(ctx)
	if err != nil {
		return err
	}
	for _, round := range rounds {
		rt := round
		rows, err := w.store.Rows(ctx, Query{Since: round.Add(-time.Minute), Entry: p.Entry, Round: &rt})
		if err != nil {
			return err
		}
		protocols := map[string]bool{}
		for _, r := range rows {
			if r.Layer == LayerProtocol {
				protocols[r.Vantage] = true
			}
		}
		if len(protocols) < len(slots) && now.Sub(round) < verdictAfter {
			continue // the points' tasks are still out
		}
		minutes, err := w.store.Rows(ctx, Query{Since: round, Until: round.Add(Every), Entry: p.Entry, Layer: LayerEntry})
		if err != nil {
			return err
		}
		v := judgeRound(rows, minutes, ru)
		var d map[string]any
		raw, _ := json.Marshal(v)
		_ = json.Unmarshal(raw, &d)
		if err := w.store.Insert(ctx, Run{TS: now.UTC(), Round: &rt, Entry: p.Entry, Exit: p.Exit, Layer: LayerVerdict,
			OK: boolp(v.FirstRed == ""), Data: d}); err != nil {
			return err
		}
		if v.FirstRed != "" {
			w.alert(ctx, p, "cascade.red."+v.FirstRed, fmt.Sprintf("раунд %s %s → %s: первым красный слой %s: %s",
				round.Format("15:04"), p.Entry, p.Exit, v.FirstRed, strings.Join(v.Red[v.FirstRed], "; ")))
		}
		if len(v.Red[LayerProtocol]) > 0 {
			prev, err := w.store.Rows(ctx, Query{Since: round.Add(-3 * Every), Until: round, Entry: p.Entry, Layer: LayerVerdict, Limit: 1, Newest: true})
			if err == nil && len(prev) == 1 && protocolRed(prev[0]) {
				w.alert(ctx, p, "cascade.protocol_red", fmt.Sprintf("протокол через вход %s → %s красный два раунда подряд (%s, %s): %s",
					p.Entry, p.Exit, prev[0].Round.Format("15:04"), round.Format("15:04"), strings.Join(v.Red[LayerProtocol], "; ")))
			}
		}
	}
	return nil
}

func protocolRed(r Run) bool {
	red, _ := r.Data["red"].(map[string]any)
	list, _ := red[LayerProtocol].([]any)
	return len(list) > 0
}
