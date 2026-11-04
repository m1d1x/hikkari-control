package stand

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/m1d1x/hikkari-control/internal/relayruns"
)

func testPolicy() Policy {
	return Policy{Mode: "auto", Window: 90 * time.Minute, MinRounds: 6, Low: 0.85, High: 0.95, Margin: 0.10,
		Cooldown: time.Hour, Control: []string{"lv-main"}}
}

func share(answered, rounds int) Share {
	return Share{Rounds: rounds, Answered: answered, Asked: rounds * Burst}
}

var fpCandidates = []string{"chrome", "chrome_155", "firefox", "safari"}

func TestDecide(t *testing.T) {
	now := time.Date(2026, 11, 4, 15, 0, 0, 0, time.UTC)
	healthy := share(298, 6)
	for _, c := range []struct {
		name       string
		current    string
		ev         Evidence
		lastSwitch time.Time
		policy     func(*Policy)
		kind, to   string
	}{
		{name: "too few rounds of the current value is no evidence", current: "chrome",
			ev: Evidence{Russia: map[string]Share{"chrome": share(0, 5), "firefox": share(250, 5)}}},
		{name: "the current value passes", current: "firefox",
			ev: Evidence{Russia: map[string]Share{"firefox": share(270, 6), "safari": healthy}}},
		{name: "cut value, the best candidate wins", current: "chrome",
			ev: Evidence{Russia: map[string]Share{"chrome": share(240, 6), "chrome_155": share(287, 6),
				"firefox": share(296, 6), "safari": share(290, 6)}, Baseline: healthy},
			kind: relayruns.EventSwitch, to: "firefox"},
		{name: "a candidate the server refuses outside Russia is not taken", current: "chrome",
			ev: Evidence{Russia: map[string]Share{"chrome": share(150, 6), "safari": share(296, 6), "firefox": share(290, 6)},
				Control: map[string]Share{"safari": share(0, 6), "firefox": share(295, 6)}},
			kind: relayruns.EventSwitch, to: "firefox"},
		{name: "a candidate measured too little is not taken", current: "chrome",
			ev:   Evidence{Russia: map[string]Share{"chrome": share(150, 6), "firefox": share(150, 3)}},
			kind: relayruns.EventAllRed},
		{name: "every candidate red", current: "chrome",
			ev:   Evidence{Russia: map[string]Share{"chrome": share(150, 6), "firefox": share(200, 6), "safari": share(260, 6)}},
			kind: relayruns.EventAllRed},
		{name: "a candidate ahead by less than the margin is not taken", current: "chrome",
			policy: func(p *Policy) { p.Low = 0.9 },
			ev:     Evidence{Russia: map[string]Share{"chrome": share(264, 6), "firefox": share(288, 6)}},
			kind:   relayruns.EventAllRed},
		{name: "the address cut: no knob helps", current: "chrome",
			ev:   Evidence{Russia: map[string]Share{"chrome": share(0, 6), "firefox": share(0, 6)}, Baseline: share(20, 6)},
			kind: relayruns.EventAddressCut},
		{name: "within the cooldown nothing switches", current: "chrome", lastSwitch: now.Add(-30 * time.Minute),
			ev: Evidence{Russia: map[string]Share{"chrome": share(100, 6), "firefox": healthy}}},
		{name: "after the cooldown it switches", current: "chrome", lastSwitch: now.Add(-61 * time.Minute),
			ev:   Evidence{Russia: map[string]Share{"chrome": share(100, 6), "firefox": healthy}},
			kind: relayruns.EventSwitch, to: "firefox"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := testPolicy()
			if c.policy != nil {
				c.policy(&p)
			}
			d := Decide(p, c.current, fpCandidates, c.ev, c.lastSwitch, now)
			if d.Kind != c.kind || d.To != c.to {
				t.Fatalf("got %q→%q (%s), want %q→%q", d.Kind, d.To, d.Reason, c.kind, c.to)
			}
			if d.Kind != "" && d.Reason == "" {
				t.Fatal("a decision without a reason")
			}
		})
	}
}

func TestALPNValuesConvert(t *testing.T) {
	if nodeValue("alpn", "h2/http1.1") != "h2,http/1.1" || standValue("alpn", "h2,http/1.1") != "h2/http1.1" {
		t.Fatal("alpn")
	}
	if currentValue("fp", "") != "chrome" || currentValue("sni", "") != "" || currentValue("fp", "firefox") != "firefox" {
		t.Fatal("current")
	}
}

// seedNode is a live node of label on a relay of its own.
func seedNode(t *testing.T, pool *pgxpool.Pool, slug, label, fingerprint string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	exp := now.Add(30 * 24 * time.Hour)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO entry_boxes (name, address_ct, provider, location, proto) VALUES ($1, '\x00', 'vdsina', 'amsterdam', 'vless-reality-vision')`,
			[]any{"ams3"}},
		{`INSERT INTO entry_relays (name, kind, status, key_id, envelope_ct, issued_at, expires_at, signed_at, box, identity_ct)
		  VALUES ($1, 'personal', 'alive', 'k1', '\x00', $2, $3, $2, $4, '\x00')`, []any{"ams3-1", now, exp, "ams3"}},
		{`INSERT INTO entry_nodes (slug, relay_id, proto, region, label, fingerprint)
		  SELECT $1, id, 'vless-reality-vision', 'eu', $2, NULLIF($3, '') FROM entry_relays WHERE name = $4`,
			[]any{slug, label, fingerprint, "ams3-1"}},
	} {
		if _, err := pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("seed node: %v", err)
		}
	}
}

// seedRuns writes rounds of one value of fp from vantage: answered of 50 each.
func seedRuns(t *testing.T, store *relayruns.Store, vantage, profile, knob, value string, answered ...int) {
	t.Helper()
	var rows []relayruns.Row
	for i, a := range answered {
		r := relayruns.Row{Source: relayruns.SourceStand, MeasuredAt: time.Now().Add(-time.Duration(i+1) * time.Minute),
			Vantage: vantage, Profile: profile, Knob: knob, Value: value, Round: i + 1, Dl: Bytes, DlEnd: "ok",
			Burst: a, BurstTotal: Burst, After: 3, AfterTotal: 3}
		if knob == Baseline {
			r.After, r.AfterTotal = 0, 0
		}
		rows = append(rows, r)
	}
	if _, err := store.Insert(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
}

func TestDBControllerSwitchesACutNodeAndSaysWhy(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	store := relayruns.NewStore(pool)
	seedNode(t, pool, "n-ams", "amsterdam-1-002", "")
	six := func(a int) []int { return []int{a, a, a, a, a, a} }
	seedRuns(t, store, "ru-spb", "amsterdam-1-002", Baseline, "no-protocol", six(50)...)
	seedRuns(t, store, "ru-spb", "amsterdam-1-002", "fp", "chrome", six(30)...)
	seedRuns(t, store, "ru-spb", "amsterdam-1-002", "fp", "chrome_155", six(48)...)
	seedRuns(t, store, "ru-spb", "amsterdam-1-002", "fp", "firefox", six(49)...)
	// Best in Russia, but the server refuses it: not taken.
	seedRuns(t, store, "ru-spb", "amsterdam-1-002", "fp", "safari", six(50)...)
	seedRuns(t, store, "lv-main", "amsterdam-1-002", "fp", "safari", six(0)...)
	// A hand run outside the active slots does not count, however red.
	seedRuns(t, store, "spb", "amsterdam-1-002", "fp", "firefox", six(0)...)

	c := NewController(pool, fakeSlots{"lv-main", "ru-spb"})
	if err := c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var fp string
	if err := pool.QueryRow(ctx, `SELECT coalesce(fingerprint, '') FROM entry_nodes WHERE slug = 'n-ams'`).Scan(&fp); err != nil || fp != "firefox" {
		t.Fatalf("fingerprint %q err %v", fp, err)
	}
	evs, err := store.KnobEvents(ctx, "amsterdam-1-002", "fp", time.Now().Add(-time.Hour), 0)
	if err != nil || len(evs) != 1 {
		t.Fatalf("events %+v err %v", evs, err)
	}
	ev := evs[0]
	if ev.Kind != relayruns.EventSwitch || ev.From != "chrome" || ev.To != "firefox" || ev.Node != "n-ams" ||
		!strings.Contains(ev.Reason, "chrome") || !strings.Contains(string(ev.Evidence), `"russia"`) {
		t.Fatalf("event %+v", ev)
	}
	// The next tick sees firefox passing: nothing more.
	if err := c.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if evs, _ := store.KnobEvents(ctx, "", "", time.Now().Add(-time.Hour), 0); len(evs) != 1 {
		t.Fatalf("a second line after the switch: %+v", evs)
	}
}

func TestDBControllerSaysAllRedOnceAndLeavesTheNode(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	store := relayruns.NewStore(pool)
	seedNode(t, pool, "n-ams", "amsterdam-1-002", "firefox")
	six := func(a int) []int { return []int{a, a, a, a, a, a} }
	for _, v := range fpCandidates {
		seedRuns(t, store, "ru-spb", "amsterdam-1-002", "fp", v, six(20)...)
	}
	c := NewController(pool, fakeSlots{"lv-main", "ru-spb"})
	for range 3 {
		if err := c.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var fp string
	_ = pool.QueryRow(ctx, `SELECT fingerprint FROM entry_nodes WHERE slug = 'n-ams'`).Scan(&fp)
	if fp != "firefox" {
		t.Fatalf("node moved to %q with every candidate red", fp)
	}
	evs, _ := store.KnobEvents(ctx, "", "", time.Now().Add(-time.Hour), 0)
	if len(evs) != 1 || evs[0].Kind != relayruns.EventAllRed {
		t.Fatalf("want one all_red line, got %+v", evs)
	}
}

func TestDBControllerOffTouchesNothing(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	store := relayruns.NewStore(pool)
	seedNode(t, pool, "n-ams", "amsterdam-1-002", "")
	six := func(a int) []int { return []int{a, a, a, a, a, a} }
	seedRuns(t, store, "ru-spb", "amsterdam-1-002", "fp", "chrome", six(10)...)
	seedRuns(t, store, "ru-spb", "amsterdam-1-002", "fp", "firefox", six(50)...)
	if _, err := pool.Exec(ctx, `UPDATE stand_settings SET controller = 'off'`); err != nil {
		t.Fatal(err)
	}
	if err := NewController(pool, fakeSlots{"lv-main", "ru-spb"}).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var fp *string
	_ = pool.QueryRow(ctx, `SELECT fingerprint FROM entry_nodes WHERE slug = 'n-ams'`).Scan(&fp)
	if fp != nil {
		t.Fatalf("switched to %q with the controller off", *fp)
	}
}

// seedPoints names the network of each point of the stand.
func seedPoints(t *testing.T, pool *pgxpool.Pool, networks map[string]string) {
	t.Helper()
	for slot, network := range networks {
		if _, err := pool.Exec(context.Background(), `INSERT INTO entry_vantages (slot, title, network) VALUES ($1, $1, NULLIF($2, ''))`,
			slot, network); err != nil {
			t.Fatalf("seed point: %v", err)
		}
	}
}

func TestDBControllerSetsANetworksValueOnItsOwnPoints(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	store := relayruns.NewStore(pool)
	seedNode(t, pool, "n-ams", "amsterdam-1-002", "")
	seedPoints(t, pool, map[string]string{"ru-spb": "AS41733", "ru-msk": "AS12389", "lv-main": ""})
	six := func(a int) []int { return []int{a, a, a, a, a, a} }
	// Russia as a whole passes chrome (540 of 600), Moscow's network does not.
	seedRuns(t, store, "ru-spb", "amsterdam-1-002", "fp", "chrome", six(50)...)
	seedRuns(t, store, "ru-msk", "amsterdam-1-002", "fp", "chrome", six(40)...)
	seedRuns(t, store, "ru-spb", "amsterdam-1-002", "fp", "firefox", six(49)...)
	seedRuns(t, store, "ru-msk", "amsterdam-1-002", "fp", "firefox", six(49)...)

	c := NewController(pool, fakeSlots{"lv-main", "ru-msk", "ru-spb"})
	for range 2 {
		if err := c.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var own *string
	if err := pool.QueryRow(ctx, `SELECT fingerprint FROM entry_nodes WHERE slug = 'n-ams'`).Scan(&own); err != nil || own != nil {
		t.Fatalf("the node's own value moved: %v err %v", own, err)
	}
	var theirs string
	if err := pool.QueryRow(ctx, `SELECT fingerprint FROM entry_node_network_knobs WHERE slug = 'n-ams' AND asn = 12389`).Scan(&theirs); err != nil || theirs != "firefox" {
		t.Fatalf("AS12389 got %q err %v", theirs, err)
	}
	var others int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM entry_node_network_knobs WHERE asn <> 12389`).Scan(&others)
	if others != 0 {
		t.Fatalf("%d values for networks that pass", others)
	}
	evs, err := store.KnobEvents(ctx, "", "", time.Now().Add(-time.Hour), 0)
	if err != nil || len(evs) != 1 {
		t.Fatalf("events %+v err %v", evs, err)
	}
	if ev := evs[0]; ev.Kind != relayruns.EventSwitch || ev.ASN != 12389 || ev.From != "chrome" || ev.To != "firefox" ||
		!strings.Contains(ev.Reason, "AS12389") {
		t.Fatalf("event %+v", ev)
	}
	nodes, err := c.Nodes(ctx)
	if err != nil || len(nodes) != 1 || nodes[0].Networks["AS12389"]["fp"] != "firefox" || nodes[0].Values["fp"] != "chrome" {
		t.Fatalf("nodes %+v err %v", nodes, err)
	}
}

func TestDBControllerOneNetworkIsTheNodesOwn(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	store := relayruns.NewStore(pool)
	seedNode(t, pool, "n-ams", "amsterdam-1-002", "")
	seedPoints(t, pool, map[string]string{"ru-spb": "AS41733", "lv-main": "AS9002"})
	six := func(a int) []int { return []int{a, a, a, a, a, a} }
	seedRuns(t, store, "ru-spb", "amsterdam-1-002", "fp", "chrome", six(30)...)
	seedRuns(t, store, "ru-spb", "amsterdam-1-002", "fp", "firefox", six(49)...)
	if err := NewController(pool, fakeSlots{"lv-main", "ru-spb"}).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var fp string
	if err := pool.QueryRow(ctx, `SELECT coalesce(fingerprint, '') FROM entry_nodes WHERE slug = 'n-ams'`).Scan(&fp); err != nil || fp != "firefox" {
		t.Fatalf("fingerprint %q err %v", fp, err)
	}
	var networks int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM entry_node_network_knobs`).Scan(&networks)
	if networks != 0 {
		t.Fatalf("a network value beside the node's own: %d", networks)
	}
}

func TestDBControllerSaysANetworksTestersSeeItRed(t *testing.T) {
	ctx := context.Background()
	pool := dbPool(t)
	store := relayruns.NewStore(pool)
	seedNode(t, pool, "n-ams", "amsterdam-1-002", "firefox")
	seedPoints(t, pool, map[string]string{"ru-spb": "AS41733", "lv-main": ""})
	var rows []relayruns.Row
	for i := range 6 {
		for _, tc := range []struct {
			network, fp string
			burst       int
		}{
			{"AS8359", "firefox", 10}, // the network's testers: red
			{"AS8359", "chrome", 50},  // a value the node does not give: not evidence
			{"AS25159", "firefox", 49},
		} {
			rows = append(rows, relayruns.Row{Source: relayruns.SourceTester, MeasuredAt: time.Now().Add(-time.Duration(i+1) * 30 * time.Minute),
				Vantage: "t-" + tc.network + tc.fp, Network: tc.network, Profile: "amsterdam-1-002", Proto: "vless-reality",
				Params: map[string]string{"fp": tc.fp}, Dl: Bytes, DlEnd: "ok", Burst: tc.burst, BurstTotal: Burst, After: 3, AfterTotal: 3})
		}
	}
	if _, err := store.Insert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	c := NewController(pool, fakeSlots{"lv-main", "ru-spb"})
	for range 3 {
		if err := c.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := store.KnobEvents(ctx, "", "", time.Now().Add(-time.Hour), 0)
	if err != nil || len(evs) != 1 {
		t.Fatalf("events %+v err %v", evs, err)
	}
	if ev := evs[0]; ev.Kind != relayruns.EventNetworkRed || ev.ASN != 8359 || ev.From != "firefox" || !strings.Contains(string(ev.Evidence), "testers") {
		t.Fatalf("event %+v", ev)
	}
	var fp string
	var networks int
	_ = pool.QueryRow(ctx, `SELECT fingerprint FROM entry_nodes WHERE slug = 'n-ams'`).Scan(&fp)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM entry_node_network_knobs`).Scan(&networks)
	if fp != "firefox" || networks != 0 {
		t.Fatalf("testers' rows moved a value: fp %q, %d network values", fp, networks)
	}
}
