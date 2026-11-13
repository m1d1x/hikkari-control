package cascadewatch

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m1d1x/hikkari-control/internal/db"
	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/stand"
	"github.com/m1d1x/hikkari-control/internal/telemetry"
)

const journal = `1794213568.844108 spb-3 nekko-trunk-entry[19617]: 2026/11/09 11:39:28 nekko-trunk-entry: members=32 joined=38 stalled=4 broken=2 resent=1 dead_ports=2 port_kills=4 streams=0/624 reset=71 up=919317 down=92481043 flows=1 dgrams=10/9 copies=3 rtt_p50=61.5 rtt_p90=80.0 rtt_max=120.2
1794213570.100000 spb-3 nekko-trunk-entry[19617]: 2026/11/09 11:39:30 trunk: member 7: down after 41m47s: trunk: member stalled
1794213571.100000 spb-3 nekko-trunk-entry[19617]: 2026/11/09 11:39:31 trunk: member 3: dial: dial tcp: i/o timeout
1794213571.500000 spb-3 nekko-trunk-entry[19617]: 2026/11/09 11:39:31 trunk: paused 1.25s
1794213572.100000 spb-3 nekko-trunk-entry[19617]: 2026/11/09 11:39:32 trunk: exit epoch 0011223344556677 after 8899aabbccddeeff: streams reset
1794213573.100000 spb-3 nekko-trunk-entry[19617]: 2026/11/09 11:39:33 trunk: exit epoch 0011223344556677
1794213574.100000 spb-3 systemd[1]: nekko-trunk-entry.service: Main process exited, code=killed, status=9/KILL
1794213575.100000 spb-3 nekko-trunk-entry[19700]: 2026/11/09 11:39:35 nekko-trunk-entry: members=0 joined=0 stalled=0 broken=0 resent=0 dead_ports=0 port_kills=0 streams=0/0 reset=0 up=0 down=0 flows=0 dgrams=0/0 copies=0 rtt_p50=- rtt_p90=- rtt_max=-
garbage line
`

func TestJournalReadsTheEntrysLines(t *testing.T) {
	lines := parseJournal(journal)
	if len(lines) != 8 {
		t.Fatalf("%d lines", len(lines))
	}
	if !lines[0].TS.Equal(time.Unix(1794213568, 844108000)) {
		t.Fatalf("ts %v", lines[0].TS)
	}
	var kinds []string
	for _, l := range lines {
		layer, d, ok := classify(l)
		if !ok {
			t.Fatalf("not read: %q", l.Msg)
		}
		if layer == LayerEntry {
			kinds = append(kinds, "stats")
			continue
		}
		kinds = append(kinds, d["kind"].(string))
	}
	if strings.Join(kinds, ",") != "stats,member_down,member_dial_failed,entry_paused,exit_epoch_changed,exit_epoch,unit,stats" {
		t.Fatalf("kinds %v", kinds)
	}
	_, st, _ := classify(lines[0])
	if num(st, "members") != 32 || num(st, "streams_opened") != 624 || num(st, "dgrams_in") != 9 || num(st, "rtt_p50_ms") != 61.5 || num(st, "copies") != 3 {
		t.Fatalf("stats %v", st)
	}
	if _, p, _ := classify(lines[3]); num(p, "ms") != 1250 {
		t.Fatalf("paused %v", p)
	}
	_, down, _ := classify(lines[1])
	if down["why"] != "stalled" || num(down, "after_s") != 41*60+47 || num(down, "member") != 7 {
		t.Fatalf("member down %v", down)
	}
	_, empty, _ := classify(lines[6])
	if has(empty, "rtt_p50_ms") {
		t.Fatalf("no members, no round trip: %v", empty)
	}
}

func TestLayersReadRedWhereAUserWouldNotice(t *testing.T) {
	ok, _ := judgeVPS(map[string]any{"cpus": 2.0, "steal_pct": 3.0, "load1": 0.5, "mem_total_mb": 2000.0, "mem_avail_mb": 900.0}, nil)
	if !ok {
		t.Fatal("a quiet box read red")
	}
	cur := map[string]any{"cpus": 1.0, "steal_pct": 47.0, "load1": 0.5, "mem_total_mb": 2000.0, "mem_avail_mb": 900.0,
		"iface": "eth0", "uptime_sec": 200.0, "rx_packets": 101000.0, "rx_drop": 1000.0}
	prev := map[string]any{"iface": "eth0", "uptime_sec": 100.0, "rx_packets": 1000.0, "rx_drop": 0.0}
	if ok, why := judgeVPS(cur, prev); ok || !strings.Contains(why, "steal 47% за 1 с") || !strings.Contains(why, "теряет 1.0% входящих") {
		t.Fatalf("eun16-like box: %v %q", ok, why)
	}
	if num(cur, "d_rx_drop") != 1000 {
		t.Fatalf("differences not kept: %v", cur)
	}
	// A second's steal of 45 % on a box whose steal over the interval is 7 %
	// is not a bad box.
	cur = map[string]any{"cpus": 2.0, "steal_pct": 45.0, "ticks_total": 2000.0, "ticks_steal": 170.0, "ticks_idle": 1300.0, "uptime_sec": 200.0}
	prev = map[string]any{"ticks_total": 1000.0, "ticks_steal": 100.0, "ticks_idle": 600.0, "uptime_sec": 100.0}
	if ok, why := judgeVPS(cur, prev); !ok || num(cur, "steal_avg_pct") != 7 || num(cur, "busy_avg_pct") != 23 {
		t.Fatalf("steal over the interval: %v %q %v", ok, why, cur)
	}
	if ok, _ := judgePath(map[string]any{"flows": 12.0, "opened": 12.0, "ping_sent": 20.0, "ping_recv": 0.0}); !ok {
		t.Fatal("ICMP filtered by the exit read as loss")
	}
	if ok, why := judgePath(map[string]any{"flows": 12.0, "opened": 9.0, "ping_sent": 20.0, "ping_recv": 20.0}); ok || why != "TCP 9/12" {
		t.Fatalf("flows lost by source port: %v %q", ok, why)
	}
	if ok, _ := judgeTrunk(map[string]any{"mode": "pool", "chunks": 48.0, "delivered": 47.0}); ok {
		t.Fatal("a lost chunk read green")
	}
	good := map[string]any{"dl": 1048576.0, "dl_total": 1048576.0, "dl_end": "ok", "burst": 50.0, "burst_total": 50.0, "after": 3.0, "after_total": 3.0,
		"voice_sent": 1500.0, "voice_got": 1495.0}
	if ok, why := judgeProtocol(good); !ok {
		t.Fatal(why)
	}
	good["voice_got"] = 1000.0
	good["burst"] = 30.0
	if ok, why := judgeProtocol(good); ok || !strings.Contains(why, "залп 30/50") || !strings.Contains(why, "звонок теряет 33%") {
		t.Fatalf("%v %q", ok, why)
	}
}

// The first layer from the bottom is to blame; what is red off the cascade
// (direct, the control vantage) is a note, not a cause.
func TestJudgeRoundBlamesTheFirstRedLayer(t *testing.T) {
	r := func(layer, vantage, target string, ok bool, why string) Run {
		return Run{Layer: layer, Vantage: vantage, Target: target, OK: boolp(ok), Data: map[string]any{"why": why}}
	}
	ru := map[string]bool{"ru-eurodir": true}
	rows := []Run{
		r(LayerVPS, "entry", "", true, ""), r(LayerVPS, "exit", "", true, ""),
		r(LayerPath, "entry", "exit", true, ""), r(LayerPath, "lv-main", "exit", false, "TCP 0/12"),
		r(LayerTrunk, "entry", "direct", false, "pool: 40 из 48"), r(LayerTrunk, "entry", "entry", false, "pool: 46 из 48"),
		r(LayerProtocol, "ru-eurodir", "entry", false, "залп 20/50"), r(LayerProtocol, "lv-main", "entry", false, "залп 1/50"),
	}
	v := judgeRound(rows, []Run{{TS: time.Now(), Data: map[string]any{"members": 32.0}}}, ru)
	if v.FirstRed != LayerTrunk || len(v.Red[LayerTrunk]) != 1 || len(v.Red[LayerProtocol]) != 1 || len(v.Notes) != 3 || len(v.Missing) != 0 {
		t.Fatalf("%+v", v)
	}
	v = judgeRound(rows[:4], []Run{{TS: time.Now(), Data: map[string]any{"members": 8.0}}}, ru)
	if v.FirstRed != LayerTrunk || !strings.Contains(v.Red[LayerTrunk][0], "членов 8") || len(v.Missing) != 1 || v.Missing[0] != LayerProtocol {
		t.Fatalf("thin pool: %+v", v)
	}
	if v := judgeRound(rows[:3], nil, ru); v.FirstRed != "" || v.Red != nil {
		t.Fatalf("a green round: %+v", v)
	}
}

func TestSummaryCountsGrowthAcrossARestart(t *testing.T) {
	t0 := time.Unix(1794213568, 0)
	m := func(i int, d map[string]any) Run {
		return Run{TS: t0.Add(time.Duration(i) * time.Minute), Entry: "e", Exit: "x", Layer: LayerEntry, Data: d}
	}
	rows := []Run{
		m(0, map[string]any{"members": 32.0, "joined": 33.0, "stalled": 1.0, "resent": 1.0, "rtt_p50_ms": 60.0}),
		m(1, map[string]any{"members": 31.0, "joined": 35.0, "stalled": 3.0, "resent": 4.0, "rtt_p50_ms": 70.0}),
		m(2, map[string]any{"members": 32.0, "joined": 32.0, "stalled": 0.0, "resent": 2.0, "rtt_p50_ms": 65.0}), // restarted
		{TS: t0, Entry: "e", Exit: "x", Layer: LayerEvent, Data: map[string]any{"kind": "member_down", "after_s": 120.0, "why": "stalled"}},
		{TS: t0, Entry: "e", Exit: "x", Layer: LayerVerdict, OK: boolp(false), Data: map[string]any{"first_red": "path"}},
		{TS: t0, Entry: "e", Exit: "x", Layer: LayerTrunk, Vantage: "entry", Target: "entry", OK: boolp(true), Data: map[string]any{"mode": "pool", "goodput_mbps": 150.0}},
	}
	s := Summarize(rows)["e → x"].(map[string]any)
	e := s["entry"].(map[string]any)
	grown := e["grown"].(map[string]int64)
	if grown["stalled"] != 2 || grown["resent"] != 5 || grown["joined"] != 34 || e["restarts"] != 1 || e["rtt_p50_ms_median"] != 65.0 {
		t.Fatalf("entry %v", e)
	}
	if s["verdicts"].(map[string]any)["first_red"].(map[string]int)["path"] != 1 {
		t.Fatalf("verdicts %v", s["verdicts"])
	}
	tr := s["layers"].(map[string]any)["trunk entry entry"].(map[string]any)["metrics"].(map[string]any)["pool.goodput_mbps"]
	if tr.(map[string]float64)["median"] != 150 {
		t.Fatalf("trunk %v", tr)
	}
}

// ── a round on fake boxes ──

func dbPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	adminURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if adminURL == "" {
		t.Skip("no test DB — set TEST_DATABASE_URL to run")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Skipf("cannot reach test DB: %v", err)
	}
	t.Cleanup(admin.Close)
	name := fmt.Sprintf("hikkari_cascade_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)") })
	cfg, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

type fakeFleet struct{ pair entry.TrunkPair }

func (f fakeFleet) TrunkPairs(context.Context) ([]entry.TrunkPair, error) {
	return []entry.TrunkPair{f.pair}, nil
}
func (f fakeFleet) RelayStatuses(context.Context, string) (map[string]string, error) {
	return map[string]string{"unl1-2": "testing"}, nil
}
func (f fakeFleet) VantageBox(_ context.Context, slot string) (string, error) {
	switch slot {
	case "lv-main":
		return "lvbox", nil
	case "ru-onentry":
		return f.pair.Entry, nil
	}
	return "", nil
}

// fakeBox answers the probe as a box would; poolDelivered is what the pool
// through the entry gets of 48.
type fakeBox struct {
	name          string
	mu            *sync.Mutex
	cmds          *[]string
	journal       string
	poolDelivered int
	steal         float64
}

func (b fakeBox) Run(_ context.Context, cmd string, _ io.Reader) (string, error) {
	b.mu.Lock()
	*b.cmds = append(*b.cmds, b.name+": "+cmd)
	b.mu.Unlock()
	switch {
	case strings.HasPrefix(cmd, "journalctl"):
		return b.journal, nil
	case strings.HasSuffix(cmd, " vps"):
		return fmt.Sprintf(`{"cpus":2,"steal_pct":%g,"load1":0.3,"mem_total_mb":2000,"mem_avail_mb":800,"iface":"eth0","uptime_sec":1000}`, b.steal), nil
	case strings.Contains(cmd, " path "):
		return `{"target":"203.0.113.9:443","flows":12,"opened":12,"ping_sent":20,"ping_recv":20,"ping_avg_ms":40}`, nil
	case strings.Contains(cmd, " trunk "):
		delivered := 48
		if strings.Contains(cmd, "pool 127.0.0.1:") {
			delivered = b.poolDelivered
		}
		mode := strings.Fields(cmd)[2]
		return fmt.Sprintf(`{"target":"x","mode":%q,"chunks":48,"delivered":%d,"first_ok":%d,"ends":{"ok":%d},"goodput_mbps":100}`, mode, delivered, delivered, delivered), nil
	case strings.HasPrefix(cmd, "pgrep"):
		return "", nil
	}
	return "", nil
}
func (b fakeBox) HostKey() string { return "" }
func (b fakeBox) Close() error    { return nil }

type fakeBoxes struct {
	mu      sync.Mutex
	cmds    []string
	journal string
	pool    int
	down    map[string]bool
}

func (f *fakeBoxes) WatchRemote(_ context.Context, name string) (entry.Remote, error) {
	if f.down[name] {
		return nil, fmt.Errorf("dial %s: connection refused", name)
	}
	steal := 2.0
	return fakeBox{name: name, mu: &f.mu, cmds: &f.cmds, journal: f.journal, poolDelivered: f.pool, steal: steal}, nil
}
func (f *fakeBoxes) StageCascadeProbe(context.Context, entry.Remote) (string, error) {
	return entry.CascadeProbeBinary, nil
}

type fakeLinks struct{ port int }

func (f *fakeLinks) CascadeProbeLinks(_ context.Context, account, entryBox, exitBox string, port, _ int) ([]string, error) {
	f.port = port
	return []string{account + " " + entryBox + " " + exitBox}, nil
}

type fakeQueue struct {
	slots []string
	loads []fakeLoad
	busy  bool
}

func (f *fakeQueue) QueueLoad(_ context.Context, slot, entryBox string, spec stand.LoadSpec) (uuid.UUID, error) {
	if f.busy {
		return uuid.Nil, stand.ErrLoadBusy
	}
	f.loads = append(f.loads, fakeLoad{slot, entryBox, spec})
	return uuid.New(), nil
}

type fakeLoad struct {
	slot, entry string
	spec        stand.LoadSpec
}

func (f *fakeQueue) QueueCascade(_ context.Context, _ string, _ time.Time, slots []string) (int, error) {
	f.slots = slots
	return len(slots), nil
}

type fakeErrs struct {
	mu   sync.Mutex
	rows []telemetry.ErrorRow
}

func (f *fakeErrs) InsertTelemetry(_ context.Context, _ []telemetry.EventRow, errs []telemetry.ErrorRow) error {
	for _, e := range errs {
		if len(e.FP) != 16 {
			return fmt.Errorf("fp %q: the errors table takes 16", e.FP)
		}
	}
	f.mu.Lock()
	f.rows = append(f.rows, errs...)
	f.mu.Unlock()
	return nil
}

func (f *fakeErrs) codes() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]int{}
	for _, r := range f.rows {
		out[r.Code]++
	}
	return out
}

func newTestWatch(t *testing.T, pool *pgxpool.Pool, boxes *fakeBoxes, now *time.Time) (*Watch, *fakeQueue, *fakeLinks, *fakeErrs) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO entry_vantages (slot, title, in_russia) VALUES ('ru-eurodir', 'ru', true), ('lv-main', 'lv', false), ('eu-riga', 'riga', false)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE stand_settings SET control_slots = '{lv-main}'`); err != nil {
		t.Fatal(err)
	}
	pair := entry.TrunkPair{Entry: "ruin1", Exit: "unl1", EntryAddress: "192.0.2.1", ExitAddress: "203.0.113.9", RelayPort: 443,
		Forward: map[int]int{8443: 443, 8053: 2053}, Members: 32}
	q, l, e := &fakeQueue{}, &fakeLinks{}, &fakeErrs{}
	w := New(pool, fakeFleet{pair}, boxes, l, q, e)
	w.now = func() time.Time { return *now }
	w.sleep = func(context.Context, time.Duration) bool { return true }
	w.started = now.Add(-time.Hour)
	return w, q, l, e
}

func TestDBARoundMeasuresEveryLayerAndJudgesIt(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	now := time.Now().UTC().Truncate(Every).Add(30 * time.Second)
	boxes := &fakeBoxes{journal: journalAt(now.Add(-30*time.Second), 32), pool: 46}
	w, queue, links, errs := newTestWatch(t, pool, boxes, &now)
	pair := w.fleet.(fakeFleet).pair
	if err := w.collect(ctx, pair); err != nil {
		t.Fatal(err)
	}
	round := roundOf(now, Every)
	if err := w.Round(ctx, pair, round); err != nil {
		t.Fatal(err)
	}
	cmds := strings.Join(boxes.cmds, "\n")
	for _, want := range []string{
		"ruin1: " + entry.CascadeProbeBinary + " vps", "unl1: " + entry.CascadeProbeBinary + " vps",
		"ruin1: " + entry.CascadeProbeBinary + " path 203.0.113.9:443 12", "lvbox: " + entry.CascadeProbeBinary + " path 203.0.113.9:443 12",
		"ruin1: " + entry.CascadeProbeBinary + " trunk pool 127.0.0.1:8053 48 262144", "ruin1: " + entry.CascadeProbeBinary + " trunk single 203.0.113.9:2053",
		"lvbox: " + entry.CascadeProbeBinary + " trunk pool 203.0.113.9:2053",
	} {
		if !strings.Contains(cmds, want) {
			t.Fatalf("no %q in\n%s", want, cmds)
		}
	}
	if strings.Join(queue.slots, ",") != "lv-main,ru-eurodir" {
		t.Fatalf("protocol tasks for %v", queue.slots)
	}
	if got, err := w.CascadeLinks(ctx, "ruin1"); err != nil || links.port != 8443 || got[0] != Account+" ruin1 unl1" {
		t.Fatalf("links %v %v port %d", got, err, links.port)
	}
	// The points report: ru-eurodir's call through the entry is fine, its load is not.
	ts := now.Format(time.RFC3339)
	res := stand.Result{Core: "core", Rows: []stand.Row{
		{TS: ts, Profile: "cascade-entry-unl1-2", Knob: stand.Baseline, Value: stand.ValueNoProtocol, Dl: 1000, DlEnd: "ok"},
		{TS: ts, Profile: "cascade-entry-unl1-2", Knob: stand.LoadBurst, Value: "50", Dl: stand.Bytes, DlEnd: "ok", Burst: 20, BurstTotal: 50, After: 3},
		{TS: ts, Profile: "cascade-direct-unl1-2", Knob: stand.LoadBurst, Value: "50", Dl: stand.Bytes, DlEnd: "ok", Burst: 50, BurstTotal: 50, After: 3},
	}, Voice: []stand.CascadeVoice{{Profile: "cascade-entry-unl1-2", Sent: 1500, Got: 1499, Gaps60: 1, GapMax: 70}}}
	if err := w.CascadeFinished(ctx, "ru-eurodir", "ruin1", round, res); err != nil {
		t.Fatal(err)
	}
	// Not judged while lv-main's task is out and the round is young.
	if err := w.judgePending(ctx, pair); err != nil {
		t.Fatal(err)
	}
	if v, _ := w.store.Rows(ctx, Query{Since: round.Add(-time.Hour), Layer: LayerVerdict}); len(v) != 0 {
		t.Fatalf("judged early: %+v", v)
	}
	now = round.Add(verdictAfter + time.Minute)
	if err := w.judgePending(ctx, pair); err != nil {
		t.Fatal(err)
	}
	v, err := w.store.Rows(ctx, Query{Since: round.Add(-time.Hour), Layer: LayerVerdict})
	if err != nil || len(v) != 1 {
		t.Fatalf("verdicts %+v %v", v, err)
	}
	if v[0].Data["first_red"] != LayerTrunk || *v[0].OK {
		t.Fatalf("verdict %+v", v[0].Data)
	}
	if notes, _ := v[0].Data["notes"].([]any); len(notes) == 0 || !strings.Contains(fmt.Sprint(notes), "unl1-2 вне пула: testing") {
		t.Fatalf("the exit's canary is not said: %v", v[0].Data["notes"])
	}
	if errs.codes()["cascade.red.trunk"] != 1 {
		t.Fatalf("alerts %v", errs.codes())
	}
	prot, _ := w.store.Rows(ctx, Query{Since: round.Add(-time.Hour), Layer: LayerProtocol})
	if len(prot) != 2 {
		t.Fatalf("protocol rows %+v", prot)
	}
	for _, p := range prot {
		if p.Target == "entry" && (num(p.Data, "gaps60") != 1 || *p.OK) || p.Target == "direct" && !*p.OK {
			t.Fatalf("protocol %s %v %+v", p.Target, *p.OK, p.Data)
		}
	}
}

func journalAt(at time.Time, members int) string {
	return fmt.Sprintf("%d.000000 spb nekko-trunk-entry[1]: x nekko-trunk-entry: members=%d joined=%d stalled=0 broken=0 resent=0 dead_ports=0 port_kills=0 streams=0/0 reset=0 up=0 down=0 flows=0 dgrams=0/0 copies=0 rtt_p50=50.0 rtt_p90=60.0 rtt_max=70.0\n",
		at.Unix(), members, members)
}

// An entry with no minute line for 3 minutes, and a box the hub cannot
// reach, are said at once; a vps the hub cannot read is the vps layer's red.
func TestDBSilentEntryAndDownBox(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	now := time.Now().UTC()
	boxes := &fakeBoxes{journal: journalAt(now.Add(-10*time.Minute), 32), down: map[string]bool{}}
	w, _, _, errs := newTestWatch(t, pool, boxes, &now)
	pair := w.fleet.(fakeFleet).pair
	w.Tick(ctx)
	w.Wait()
	if errs.codes()["cascade.entry_silent"] != 1 {
		t.Fatalf("silence not said: %v", errs.codes())
	}
	w.Tick(ctx)
	w.Wait()
	if errs.codes()["cascade.entry_silent"] != 1 {
		t.Fatalf("said twice within %s: %v", alertEvery, errs.codes())
	}
	boxes.down["unl1"] = true
	round := roundOf(now, Every).Add(Every)
	if err := w.Round(ctx, pair, round); err != nil {
		t.Fatal(err)
	}
	vps, _ := w.store.Rows(ctx, Query{Since: now.Add(-time.Hour), Layer: LayerVPS})
	var downRed bool
	for _, r := range vps {
		if r.Vantage == "exit" && !*r.OK && strings.Contains(r.Data["why"].(string), "connection refused") {
			downRed = true
		}
	}
	if !downRed {
		t.Fatalf("a box down is not the vps layer's red: %+v", vps)
	}
}

// The trunk's call: a minute per call, its start its time, its pauses kept
// with their time for the summary.
func TestCallMinuteIsALayer(t *testing.T) {
	out := `1794213660.100000 spb nekko-trunk-entry[812]: 2026/11/11 00:01:02 nekko-trunk-entry: call copies=1 from=1794213600 sent=3000 got=2990 rtt_p50=48.1 rtt_p99=61.0 rtt_max=130.0 gaps60=3 gaps150=1 gap_max=540 silent_ms=700 sessions=1 worst=1794213601500:540,1794213609000:80
1794213660.200000 spb nekko-trunk-entry[812]: 2026/11/11 00:01:02 nekko-trunk-entry: call copies=2 from=1794213600 sent=3000 got=0 rtt_p50=- rtt_p99=- rtt_max=- gaps60=1 gaps150=1 gap_max=60000 silent_ms=59980 sessions=1 worst=1794213600000:60000`
	lines := parseJournal(out)
	if len(lines) != 2 {
		t.Fatalf("%d lines", len(lines))
	}
	layer, one, ok := classify(lines[0])
	if !ok || layer != LayerCall {
		t.Fatalf("not a call: %v %v", layer, ok)
	}
	if num(one, "copies") != 1 || num(one, "lost") != 10 || num(one, "rtt_p50_ms") != 48.1 || num(one, "gap_max_ms") != 540 || num(one, "silent_ms") != 700 {
		t.Fatalf("call %v", one)
	}
	worst, _ := one["worst"].([]map[string]any)
	if len(worst) != 2 || !worst[0]["at"].(time.Time).Equal(time.UnixMilli(1794213601500)) || num(worst[0], "ms") != 540 {
		t.Fatalf("worst %v", one["worst"])
	}
	_, two, _ := classify(lines[1])
	if has(two, "rtt_p50_ms") || num(two, "lost") != 3000 || callOK(two) || !callOK(map[string]any{"gaps150": 0.0}) {
		t.Fatalf("a silent minute %v", two)
	}
	from := time.Unix(1794213600, 0).UTC()
	rows := []Run{
		{TS: from, Entry: "spb", Exit: "unl1", Layer: LayerCall, Vantage: "entry", Target: "copies1", OK: boolp(callOK(one)), Data: one},
		{TS: from.Add(time.Minute), Entry: "spb", Exit: "unl1", Layer: LayerCall, Vantage: "entry", Target: "copies1", OK: boolp(true),
			Data: map[string]any{"sent": 3000.0, "lost": 0.0, "gaps150": 0.0, "gap_max_ms": 40.0, "silent_ms": 0.0}},
	}
	sum := Summarize(rows)["spb → unl1"].(map[string]any)["layers"].(map[string]any)["call entry copies1"].(map[string]any)
	if sum["n"] != 2 || sum["red"] != 1 || sum["silent_ms_total"] != 700.0 {
		t.Fatalf("summary %v", sum)
	}
	if w := sum["worst"].([]map[string]any); len(w) != 2 || num(w[0], "ms") != 540 {
		t.Fatalf("summary worst %v", sum["worst"])
	}
}

// A hub that restarts goes on reading the entry's journal from where the last
// one stopped. It used to start over from the last minute line and file the
// events after it twice (10.11 19:20 UTC: six member deaths in two rows each).
func TestDBARestartedHubFilesTheEntrysEventsOnce(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	now := time.Now().UTC().Truncate(time.Second)
	minute := now.Add(-90 * time.Second)
	death := now.Add(-30 * time.Second)
	boxes := &fakeBoxes{journal: journalAt(minute, 32) +
		fmt.Sprintf("%d.477807 spb nekko-trunk-entry[1]: x trunk: member 27: down after 9m57s: EOF\n", death.Unix())}
	w, _, _, _ := newTestWatch(t, pool, boxes, &now)
	pair := w.fleet.(fakeFleet).pair
	if err := w.collect(ctx, pair); err != nil {
		t.Fatal(err)
	}
	// The next hub: a cursor of its own, empty, and the same journal.
	again := New(pool, fakeFleet{pair}, boxes, &fakeLinks{}, &fakeQueue{}, &fakeErrs{})
	again.now = func() time.Time { return now.Add(time.Minute) }
	if err := again.collect(ctx, pair); err != nil {
		t.Fatal(err)
	}
	for layer, want := range map[string]int{LayerEvent: 1, LayerEntry: 1} {
		rows, err := w.store.Rows(ctx, Query{Since: now.Add(-time.Hour), Layer: layer})
		if err != nil || len(rows) != want {
			t.Fatalf("%s: %d rows, want %d (%v)", layer, len(rows), want, err)
		}
	}
	// A line logged after the restart is still read.
	boxes.journal += fmt.Sprintf("%d.100000 spb nekko-trunk-entry[1]: x trunk: member 9: down after 6m14s: EOF\n", now.Add(20*time.Second).Unix())
	if err := again.collect(ctx, pair); err != nil {
		t.Fatal(err)
	}
	if rows, _ := w.store.Rows(ctx, Query{Since: now.Add(-time.Hour), Layer: LayerEvent}); len(rows) != 2 {
		t.Fatalf("events after the restart: %d rows, want 2", len(rows))
	}
}

// A hitless rollout of the entry is told by its journal: who took over, what
// the old generation drained with, and what it still carried when it left;
// and the exit's, as the pool lives it: a new session on its drain, and what
// a retired session or the stopped entry cut.
func TestJournalReadsTheEntrysHandoff(t *testing.T) {
	const j = `1794213568.844108 spb-3 nekko-trunk-entry[19617]: 2026/11/09 11:39:28 nekko-trunk-entry: generation 2 (pid 4242) is up, generation 1 drains
1794213568.900000 spb-3 nekko-trunk-entry[19617]: 2026/11/09 11:39:28 nekko-trunk-entry: generation 1 drains with 3 stream(s) and 2 flow(s)
1794213569.100000 spb-3 nekko-trunk-entry[4242]: 2026/11/09 11:39:29 nekko-trunk-entry: up: generation 2, exit 203.0.113.8:443 (hk.example), 32 members, tcp [8443→443], udp [8443→443], udp one copy [8443]
1794215368.000000 spb-3 nekko-trunk-entry[19617]: 2026/11/09 12:09:28 nekko-trunk-entry: generation 1 leaves after 30m0s with 1 stream(s) and 0 flow(s)
1794215369.000000 spb-3 nekko-trunk-entry[4300]: 2026/11/09 12:09:29 nekko-trunk-entry: generation 2 carries nothing more and leaves
1794215400.000000 spb-3 nekko-trunk-entry[4300]: 2026/11/09 12:10:00 trunk: exit drains: new session, the old one keeps 2 stream(s) and 1 flow(s)
1794215410.000000 spb-3 nekko-trunk-entry[4300]: 2026/11/09 12:10:10 trunk: exit drains: the new session has 3 of 32 member(s) after 10s, it takes over anyway
1794217200.000000 spb-3 nekko-trunk-entry[4300]: 2026/11/09 12:40:00 trunk: retired session closed after 30m0s with 1 stream(s) and 3 flow(s)
1794217300.000000 spb-3 nekko-trunk-entry[4300]: 2026/11/09 12:41:40 trunk: closed with 2 stream(s) and 0 flow(s) cut
`
	want := []map[string]any{
		{"kind": "entry_handoff", "gen": 2, "was": 1},
		{"kind": "entry_drains", "gen": 1, "streams": 3, "flows": 2},
		{"kind": "entry_up"},
		{"kind": "entry_generation_left", "gen": 1, "cut_streams": 1, "cut_flows": 0},
		{"kind": "entry_generation_left", "gen": 2, "cut_streams": 0, "cut_flows": 0},
		{"kind": "exit_drains", "streams": 2, "flows": 1},
		{"kind": "exit_drain_slow_pool", "members": 3, "want": 32},
		{"kind": "retired_session_cut", "cut_streams": 1, "cut_flows": 3},
		{"kind": "entry_closed_cut", "cut_streams": 2, "cut_flows": 0},
	}
	lines := parseJournal(j)
	if len(lines) != len(want) {
		t.Fatalf("%d lines", len(lines))
	}
	for i, l := range lines {
		layer, d, ok := classify(l)
		if !ok || layer != LayerEvent {
			t.Fatalf("line %d not an event: %q", i, l.Msg)
		}
		for k, v := range want[i] {
			if fmt.Sprint(d[k]) != fmt.Sprint(v) {
				t.Fatalf("line %d: %s=%v, want %v (%v)", i, k, d[k], v, d)
			}
		}
	}
}
