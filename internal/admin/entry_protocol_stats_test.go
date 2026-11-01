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

func cents(n int) *int { return &n }

// One subscription on two machines of a protocol is one subscriber of that
// protocol and one of each machine. Summing the machines would count it twice
// and read a fleet twice as loaded as it is.
func TestProtocolSubscribersAreDistinctPerLevel(t *testing.T) {
	const reality = "vless-reality-vision"
	boxes := []protocolBoxes{
		{Name: "a1", MonthlyCents: cents(100), Relays: []protocolRelay{{Name: "a1-1", Status: "alive"}, {Name: "a1-2", Status: "alive"}}},
		{Name: "b1", MonthlyCents: cents(200), Relays: []protocolRelay{{Name: "b1-1", Status: "alive"}}},
	}
	protoOf := map[string]string{"a1": reality, "b1": reality}
	subs := []relaySubscriber{
		{"alice", "a1-1", "a1", reality},
		{"alice", "a1-2", "a1", reality},
		{"alice", "b1-1", "b1", reality},
		{"bob", "b1-1", "b1", reality},
	}
	got := rollupProtocols(boxes, protoOf, subs, relayFigures{}, time.Now())
	if len(got) != 1 {
		t.Fatalf("protocols = %d, want 1", len(got))
	}
	p := got[0]
	if p.Subscribers != 2 {
		t.Errorf("protocol subscribers = %d, want 2", p.Subscribers)
	}
	if p.Boxes[0].Subscribers != 1 || p.Boxes[1].Subscribers != 2 {
		t.Errorf("box subscribers = %d, %d, want 1, 2", p.Boxes[0].Subscribers, p.Boxes[1].Subscribers)
	}
	if r := p.Boxes[1].Relays[0].Subscribers; r != 2 {
		t.Errorf("relay subscribers = %d, want 2", r)
	}
	if p.MonthlyCents != 300 || p.BoxesUnpriced != 0 {
		t.Errorf("cost = %d (unpriced %d), want 300 (0)", p.MonthlyCents, p.BoxesUnpriced)
	}
}

// A dead relay is not the protocol's state now: its last verdict must not paint
// a protocol whose live relays get through. A machine with no relay alive is
// not live, and a stale pulse says nothing of the connections now.
func TestProtocolHealthReadsLiveRelaysOnly(t *testing.T) {
	const proto = "vless-xhttp"
	now := time.Now()
	boxes := []protocolBoxes{
		{Name: "a1", Relays: []protocolRelay{{Name: "a1-1", Status: "alive"}, {Name: "a1-2", Status: "dead"}}},
		{Name: "b1", Relays: []protocolRelay{{Name: "b1-1", Status: "pending"}}},
	}
	protoOf := map[string]string{"a1": proto, "b1": proto}
	three := uint64(3)
	data := relayFigures{
		verdict: map[string]lastVerdict{
			"a1-1": {verdict: "clean", at: now},
			"a1-2": {verdict: "ip_banned", at: now},
			"b1-1": {verdict: "fingerprint", at: now},
		},
		pulse: map[string]relayPulse{
			"a1-1": {conns: three, at: now.Add(-time.Minute)},
			"a1-2": {conns: 7, at: now.Add(-time.Hour)},
		},
	}
	p := rollupProtocols(boxes, protoOf, nil, data, now)[0]
	if p.Health != healthOK {
		t.Errorf("health = %q, want %q", p.Health, healthOK)
	}
	if p.BoxesLive != 1 || p.BoxesTotal != 2 || p.RelaysAlive != 1 || p.RelaysTotal != 3 {
		t.Errorf("live = %d/%d boxes, %d/%d relays, want 1/2, 1/3", p.BoxesLive, p.BoxesTotal, p.RelaysAlive, p.RelaysTotal)
	}
	if p.BoxesUnpriced != 2 {
		t.Errorf("unpriced = %d, want 2: a machine with no price is said, not zero", p.BoxesUnpriced)
	}
	if p.Conns != 3 {
		t.Errorf("conns = %d, want 3: the hour-old pulse does not read as now", p.Conns)
	}
	if c := p.Boxes[0].Relays[1].Conns; c != nil {
		t.Errorf("stale pulse conns = %d, want none", *c)
	}
}

func TestFoldHealth(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{nil, healthUnknown},
		{[]string{healthUnknown}, healthUnknown},
		{[]string{healthOK, healthUnknown}, healthOK},
		{[]string{healthBad, healthUnknown}, healthBad},
		{[]string{healthOK, healthBad}, healthWarn},
		{[]string{healthOK, healthWarn}, healthWarn},
	} {
		if got := foldHealth(c.in); got != c.want {
			t.Errorf("foldHealth(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// One ban among clean rounds is what the bucket shows, in whatever order the
// rows of the bucket come.
func TestVerdictHistoryKeepsTheWorstOfABucket(t *testing.T) {
	at := time.Now().Truncate(time.Hour)
	for _, order := range [][]string{{"clean", "ip_banned", "unknown"}, {"ip_banned", "unknown", "clean"}, {"unknown", "clean", "ip_banned"}} {
		var pts []verdictPoint
		for _, v := range order {
			pts = addVerdict(pts, at, v, 2)
		}
		if len(pts) != 1 || pts[0].Worst != "ip_banned" || pts[0].OK != 2 || pts[0].Bad != 2 || pts[0].Other != 2 {
			t.Errorf("order %v: %+v", order, pts)
		}
	}
}

// The whole read against both stores: what a relay carried and what the
// vantage said reach its machine and its protocol, and a machine handed back
// is not on the screen.
func TestEntryProtocolStatsReadsBothStores(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)
	ctx := context.Background()

	tag := fmt.Sprintf("ps%d", time.Now().UnixNano()%100_000)
	live := seedFleetBox(t, pool, tag+"1", "vless-reality-vision")
	gone := seedFleetBox(t, pool, tag+"2", "vless-reality-vision")
	relay := live + "-1"
	seedFleetRelay(t, pool, relay, live, "alive")
	seedFleetRelay(t, pool, gone+"-1", gone, "dead")
	if _, err := pool.Exec(ctx, `UPDATE entry_boxes SET retired_at = now(), retired_reason = 'rotated' WHERE name = $1`, gone); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	if err := store.InsertEntryRelayStats(ctx, []analytics.EntryRelayStatRecord{
		{Timestamp: now.Add(-time.Minute), RelayName: relay, Proto: "vless-reality-vision", UptimeS: 100, BytesUp: 10, BytesDown: 20, Conns: 1},
		{Timestamp: now.Add(-30 * time.Second), RelayName: relay, Proto: "vless-reality-vision", UptimeS: 130, BytesUp: 110, BytesDown: 220, Conns: 4},
	}); err != nil {
		t.Fatalf("insert stats: %v", err)
	}
	if err := store.InsertEntryProbes(ctx, []analytics.EntryProbeRecord{
		{Timestamp: now.Add(-2 * time.Minute), SourceRegion: "ru-spb", RelayName: relay, Verdict: "fingerprint"},
		{Timestamp: now.Add(-time.Minute), SourceRegion: "ru-spb", RelayName: relay, Verdict: "clean"},
	}); err != nil {
		t.Fatalf("insert probes: %v", err)
	}

	var p protocolStats
	var b protocolBoxes
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
		for _, pr := range got.Protocols {
			for _, box := range pr.Boxes {
				if box.Name == gone {
					t.Fatalf("machine %s handed back is on the screen", gone)
				}
				if box.Name == live {
					p, b = pr, box
				}
			}
		}
		if len(b.Relays) == 1 && b.Relays[0].BytesDown == 200 && b.Relays[0].Verdict == "clean" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dataset never read back: %+v", b)
		}
		time.Sleep(200 * time.Millisecond)
	}
	r := b.Relays[0]
	if r.BytesUp != 100 || r.Conns == nil || *r.Conns != 4 || r.Health != healthOK {
		t.Errorf("relay = %+v", r)
	}
	var bad, ok uint64
	for _, pt := range r.History {
		bad += pt.Bad
		ok += pt.OK
	}
	if bad != 1 || ok != 1 {
		t.Errorf("history counts bad=%d ok=%d, want 1, 1: %+v", bad, ok, r.History)
	}
	if p.BytesDown < 200 || p.Health == healthUnknown {
		t.Errorf("protocol = %+v", p)
	}
}

// Without the dataset the screen still answers, and says what it lacks.
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
	if len(got.Missing) != 2 {
		t.Fatalf("missing = %+v, want verdicts and traffic", got.Missing)
	}
}
