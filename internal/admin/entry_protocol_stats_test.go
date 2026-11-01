package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/analytics"
)

func value(t *testing.T, s []*float64, i int) float64 {
	t.Helper()
	if s[i] == nil {
		t.Fatalf("bucket %d is a gap, want a value", i)
	}
	return *s[i]
}

// The walk over four hourly buckets, the last still running. b1 is born in the
// second bucket, handed back in the third; a1 lives throughout.
func historyFixture() (starts []time.Time, now time.Time, boxes []historyBox) {
	t0 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	starts = []time.Time{t0, t0.Add(time.Hour), t0.Add(2 * time.Hour), t0.Add(3 * time.Hour)}
	now = t0.Add(3*time.Hour + 30*time.Minute)
	cost, price := 100, 200
	retired := t0.Add(2*time.Hour + 30*time.Minute)
	boxes = []historyBox{
		{name: "a1", proto: "vless-xhttp", createdAt: t0.Add(-time.Hour), monthlyCents: &cost,
			relays: []historyRelay{{name: "a1-1", createdAt: t0.Add(-time.Hour), admitted: true}}},
		{name: "b1", proto: "vless-xhttp", createdAt: t0.Add(time.Hour + 30*time.Minute), retiredAt: &retired, monthlyCents: &price,
			relays: []historyRelay{{name: "b1-1", createdAt: t0.Add(time.Hour + 30*time.Minute), admitted: true}}},
	}
	return starts, now, boxes
}

func TestProtocolHistoryReadsLifeAndCostAtEachBucketsEnd(t *testing.T) {
	starts, now, boxes := historyFixture()
	protocols, nodes := protocolHistory(starts, time.Hour, now, boxes, nil, relaySeries{})
	if len(protocols) != 1 || len(nodes) != 2 {
		t.Fatalf("protocols %d, nodes %d, want 1, 2", len(protocols), len(nodes))
	}
	p := protocols[0]
	for i, want := range []float64{1, 2, 1, 1} {
		if got := value(t, p.Live, i); got != want {
			t.Errorf("machines alive at bucket %d = %v, want %v", i, got, want)
		}
	}
	for i, want := range []float64{100, 300, 100, 100} {
		if got := value(t, p.CostCents, i); got != want {
			t.Errorf("cost at bucket %d = %v, want %v", i, got, want)
		}
	}
	b := nodes[1]
	if b.Key != "b1" || !b.Retired {
		t.Fatalf("node = %+v", b)
	}
	if b.Live[0] != nil {
		t.Errorf("b1 has a point before it existed: %v", *b.Live[0])
	}
	if value(t, b.Live, 1) != 1 || value(t, b.Live, 2) != 0 {
		t.Errorf("b1 relays alive = %v, %v, want 1, 0", *b.Live[1], *b.Live[2])
	}
	if b.Live[3] != nil {
		t.Errorf("b1 has a point after its life: %v", *b.Live[3])
	}
}

// A machine never admitted cost money but is no line of its own.
func TestProtocolHistoryLeavesOutMachinesNeverAdmitted(t *testing.T) {
	starts, now, boxes := historyFixture()
	boxes[1].relays[0].admitted = false
	protocols, nodes := protocolHistory(starts, time.Hour, now, boxes, nil, relaySeries{})
	if len(nodes) != 1 || nodes[0].Key != "a1" {
		t.Fatalf("nodes = %+v", nodes)
	}
	if got := value(t, protocols[0].CostCents, 1); got != 300 {
		t.Errorf("cost = %v, want 300: the rent was paid", got)
	}
	if got := value(t, protocols[0].Live, 1); got != 1 {
		t.Errorf("live = %v, want 1", got)
	}
}

// One subscription on two machines is one subscriber of the protocol and one of
// each machine; a grant counts only inside its interval.
func TestProtocolHistorySubscribersAreDistinctPerLevel(t *testing.T) {
	starts, now, boxes := historyFixture()
	early := starts[0].Add(-time.Hour)
	ended := starts[2].Add(30 * time.Minute)
	grants := []historyGrant{
		{"alice", "a1-1", "a1", "vless-xhttp", early, nil},
		{"alice", "b1-1", "b1", "vless-xhttp", early, nil},
		{"bob", "a1-1", "a1", "vless-xhttp", early, &ended},
	}
	protocols, nodes := protocolHistory(starts, time.Hour, now, boxes, grants, relaySeries{})
	for i, want := range []float64{2, 2, 1, 1} {
		if got := value(t, protocols[0].Subscribers, i); got != want {
			t.Errorf("protocol subscribers at %d = %v, want %v", i, got, want)
		}
	}
	if got := value(t, nodes[0].Subscribers, 1); got != 2 {
		t.Errorf("a1 subscribers = %v, want 2", got)
	}
	if got := value(t, nodes[1].Subscribers, 1); got != 1 {
		t.Errorf("b1 subscribers = %v, want 1", got)
	}
}

// A protocol's pass rate is over its probes, so a machine probed nine times
// weighs nine. A bucket with no judging probe is a gap, not 0% and not 100%.
func TestProtocolHistoryPassRateIsOverProbesAndGapsStayGaps(t *testing.T) {
	starts, now, boxes := historyFixture()
	key := starts[1].Unix()
	data := relaySeries{
		probes: map[string]map[int64]probeCount{
			"a1-1": {key: {ok: 9}},
			"b1-1": {key: {bad: 1}},
		},
		traffic: map[string]map[int64]float64{"a1-1": {key: 500}, "b1-1": {key: 25}},
		conns:   map[string]map[int64]float64{"a1-1": {key: 3}},
	}
	protocols, nodes := protocolHistory(starts, time.Hour, now, boxes, nil, data)
	p := protocols[0]
	if got := value(t, p.PassPct, 1); got != 90 {
		t.Errorf("protocol pass = %v, want 90", got)
	}
	if p.PassPct[0] != nil || p.TrafficBytes[0] != nil || p.Conns[0] != nil {
		t.Errorf("a bucket nobody measured has a value: %+v", p)
	}
	if got := value(t, p.TrafficBytes, 1); got != 525 {
		t.Errorf("protocol traffic = %v, want 525", got)
	}
	if got := value(t, nodes[1].PassPct, 1); got != 0 {
		t.Errorf("b1 pass = %v, want 0", got)
	}
}

// The whole read against both stores: what a relay carried and what the
// vantage said arrive on the right bucket, for the protocol and the machine.
func TestEntryProtocolStatsReadsBothStores(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)
	ctx := context.Background()

	box := seedFleetBox(t, pool, fmt.Sprintf("ps%d", time.Now().UnixNano()%100_000), "vless-reality-vision")
	relay := box + "-1"
	seedFleetRelay(t, pool, relay, box, "alive")

	now := time.Now().UTC().Truncate(time.Second)
	if err := store.InsertEntryRelayStats(ctx, []analytics.EntryRelayStatRecord{
		{Timestamp: now.Add(-time.Minute), RelayName: relay, Proto: "vless-reality-vision", UptimeS: 100, BytesUp: 10, BytesDown: 20, Conns: 2},
		{Timestamp: now.Add(-30 * time.Second), RelayName: relay, Proto: "vless-reality-vision", UptimeS: 130, BytesUp: 110, BytesDown: 220, Conns: 4},
	}); err != nil {
		t.Fatalf("insert stats: %v", err)
	}
	if err := store.InsertEntryProbes(ctx, []analytics.EntryProbeRecord{
		{Timestamp: now.Add(-40 * time.Second), SourceRegion: "ru-spb", RelayName: relay, Verdict: "fingerprint"},
		{Timestamp: now.Add(-30 * time.Second), SourceRegion: "ru-spb", RelayName: relay, Verdict: "clean"},
		{Timestamp: now.Add(-20 * time.Second), SourceRegion: "ru-spb", RelayName: relay, Verdict: "unknown"},
	}); err != nil {
		t.Fatalf("insert probes: %v", err)
	}

	var node protocolSeries
	deadline := time.Now().Add(15 * time.Second)
	for {
		rec := httptest.NewRecorder()
		(&Handlers{analytics: store, pool: pool}).EntryProtocolStats(rec, httptest.NewRequest(http.MethodGet, "/?window=24h", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var got entryProtocolStats
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, n := range got.Nodes {
			if n.Key == box {
				node = n
			}
		}
		if len(node.Live) == len(got.Buckets) && len(node.Live) > 0 {
			var bytes, pass, conns *float64
			for i := range node.Live {
				if node.TrafficBytes[i] != nil {
					bytes, conns = node.TrafficBytes[i], node.Conns[i]
				}
				if node.PassPct[i] != nil {
					pass = node.PassPct[i]
				}
			}
			if bytes != nil && *bytes == 300 && pass != nil {
				if *pass != 50 {
					t.Errorf("pass = %v, want 50: `unknown` does not judge", *pass)
				}
				if conns == nil || *conns != 3 {
					t.Errorf("conns = %v, want the bucket's mean, 3", conns)
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("dataset never read back: %+v", node)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Without the dataset the screen still answers, and names what it lacks.
func TestEntryProtocolStatsNamesWhatItCannotRead(t *testing.T) {
	pool := livePostgres(t)
	rec := httptest.NewRecorder()
	(&Handlers{pool: pool}).EntryProtocolStats(rec, httptest.NewRequest(http.MethodGet, "/?window=24h", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got entryProtocolStats
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Missing) < 3 {
		t.Fatalf("missing = %+v, want pass rate, traffic and connections", got.Missing)
	}
}
