package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/analytics"
	"github.com/m1d1x/hikkari-control/internal/entry"
)

func TestCarriedMbpsTakesTheBusierDirection(t *testing.T) {
	// 1 MB down and 0.25 MB up over 8 s: the link carried 1 Mbit/s, not 1.25.
	got, ok := carriedMbps(250_000, 1_000_000, 8)
	if !ok || got != 1 {
		t.Fatalf("carriedMbps = %v, %v; want 1, true", got, ok)
	}
	if _, ok := carriedMbps(10, 10, 0); ok {
		t.Fatal("no measured second must be no rate, not zero")
	}
}

func TestFoldProfilesLaysFiguresOnBucketsAndKeepsGaps(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	month := monthStart(t0)
	starts := []time.Time{t0, t0.Add(time.Hour), t0.Add(2 * time.Hour)}
	f := relayFigures{
		probes: map[string]map[int64]probeCount{"a1-1": {
			t0.Unix():                    {ok: 3, bad: 1},
			t0.Add(time.Hour).Unix():     {},
			t0.Add(2 * time.Hour).Unix(): {ok: 2},
		}},
		rtt:     map[string]map[int64]float64{"a1-1": {t0.Unix(): 300, t0.Add(2 * time.Hour).Unix(): 310}},
		carried: map[string]map[int64]float64{"a1-1": {t0.Add(time.Hour).Unix(): 4.5}},
		speed:   map[string]map[int64]float64{"ams-2-node": {t0.Unix(): 340}},
		covers:  map[string]string{"a1-1": "www.samsung.com", "b1-1": "probed.example"},
		boxGB:   map[string]float64{"a1": 1500},
	}
	got := foldProfiles([]fleetProfile{
		{Label: "riga-1", Relay: "b1-1", Box: "b1", Proto: "vless-xhttp", Cover: "site.example", boxSince: t0.Add(-time.Hour)},
		{Label: "ams-2", Slug: "ams-2-node", Relay: "a1-1", Box: "a1", Proto: "vless-reality-vision", boxSince: month.Add(-48 * time.Hour)},
		{Label: "ams-1", Relay: "c1-1", Box: "c1", Proto: "vless-reality-vision"},
	}, starts, month, f)

	if got[0].Label != "ams-1" || got[1].Label != "ams-2" || got[2].Label != "riga-1" {
		t.Fatalf("order = %s, %s, %s; want by protocol, then name", got[0].Label, got[1].Label, got[2].Label)
	}
	p := got[1]
	if value(t, p.RTTMs, 0) != 300 || p.RTTMs[1] != nil || value(t, p.RTTMs, 2) != 310 {
		t.Errorf("rtt = %v, want 300, gap, 310", p.RTTMs)
	}
	if p.CarriedMbps[0] != nil || value(t, p.CarriedMbps, 1) != 4.5 {
		t.Errorf("carried = %v, want gap, 4.5", p.CarriedMbps)
	}
	if value(t, p.SpeedMbps, 0) != 340 || p.SpeedMbps[1] != nil {
		t.Errorf("speed = %v, want 340 from the node's measurements, then gap", p.SpeedMbps)
	}
	if value(t, p.AvailPct, 0) != 75 || p.AvailPct[1] != nil || value(t, p.AvailPct, 2) != 100 {
		t.Errorf("avail = %v, want 75, gap (nobody judged), 100", p.AvailPct)
	}
	if p.Cover != "www.samsung.com" {
		t.Errorf("cover = %q, want the probed one", p.Cover)
	}
	if got[2].Cover != "site.example" {
		t.Errorf("an XHTTP box keeps its own domain, got %q", got[2].Cover)
	}
	if p.TrafficUsedGB == nil || *p.TrafficUsedGB != 1500 || !p.TrafficSince.Equal(month) {
		t.Errorf("month = %v since %v, want 1500 GB since %v", p.TrafficUsedGB, p.TrafficSince, month)
	}
	if !got[2].TrafficSince.Equal(t0.Add(-time.Hour)) {
		t.Errorf("a box born this month counts from its birth, got %v", got[2].TrafficSince)
	}
	if got[0].TrafficUsedGB == nil || *got[0].TrafficUsedGB != 0 {
		t.Errorf("a box with no deltas spent 0, got %v", got[0].TrafficUsedGB)
	}
	if len(got[0].RTTMs) != len(starts) {
		t.Errorf("a relay with no figures still gets a line of gaps, got %d points", len(got[0].RTTMs))
	}
}

func TestHelloBuildOfNamesTheBrowserTheCoreImitates(t *testing.T) {
	for _, c := range []struct{ proto, fp, want string }{
		{entry.ProtoVlessRealityVision, "", "chrome_133"},
		{entry.ProtoVlessRealityVision, "chrome_155", "chrome_155"},
		{entry.ProtoVlessXhttp, "firefox", "firefox_148"},
		{entry.ProtoHysteria2, "chrome", ""},
		{entry.ProtoAmneziaWG, "", ""},
	} {
		if got := helloBuildOf(c.proto, c.fp); got != c.want {
			t.Errorf("helloBuildOf(%s, %q) = %q, want %q", c.proto, c.fp, got, c.want)
		}
	}
}

func TestPlanQuotasCachesTheCatalogAndRetriesAFailure(t *testing.T) {
	calls := 0
	fail := true
	q := &planQuotas{cache: map[string]quotaEntry{}, read: func(ctx context.Context, kind, region string) (entry.ProviderCatalog, error) {
		calls++
		if fail {
			return entry.ProviderCatalog{}, errors.New("down")
		}
		return entry.ProviderCatalog{Plans: []entry.BoxPlan{{ID: "36", BandwidthGB: 32768}}}, nil
	}}
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	if _, ok := q.bandwidthGB(context.Background(), "vdsina", "4", "36", now); ok || calls != 1 {
		t.Fatalf("a hoster down gives no allowance, calls = %d", calls)
	}
	if _, ok := q.bandwidthGB(context.Background(), "vdsina", "4", "36", now.Add(time.Minute)); ok || calls != 1 {
		t.Fatalf("a failure is not retried on every refresh, calls = %d", calls)
	}
	fail = false
	gb, ok := q.bandwidthGB(context.Background(), "vdsina", "4", "36", now.Add(quotaRetry+time.Second))
	if !ok || gb != 32768 || calls != 2 {
		t.Fatalf("after the retry pause: %d, %v, calls = %d", gb, ok, calls)
	}
	if _, _ = q.bandwidthGB(context.Background(), "vdsina", "4", "36", now.Add(30*time.Minute)); calls != 2 {
		t.Fatalf("a fresh catalog is not asked again, calls = %d", calls)
	}
	var none *planQuotas
	if _, ok := none.bandwidthGB(context.Background(), "vdsina", "4", "36", now); ok {
		t.Fatal("no catalog wired is no allowance")
	}
}

func TestFleetProfilesReadsServedNodesAndTheirLines(t *testing.T) {
	store := liveClickHouse(t)
	pool := livePostgres(t)
	ctx := context.Background()

	box := seedFleetBox(t, pool, fmt.Sprintf("fp%d", time.Now().UnixNano()%100_000), "vless-reality-vision")
	relay := box + "-1"
	seedFleetRelay(t, pool, relay, box, "alive")
	slug := fmt.Sprintf("fp-%d", time.Now().UnixNano()%1_000_000)
	if _, err := pool.Exec(ctx, `
		INSERT INTO entry_nodes (slug, relay_id, proto, label, fingerprint)
		SELECT $1, id, 'vless-reality-vision', $1, 'firefox' FROM entry_relays WHERE name = $2`, slug, relay); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM entry_nodes WHERE slug = $1`, slug) })

	now := time.Now().UTC().Truncate(time.Second)
	if err := store.InsertEntryProbes(ctx, []analytics.EntryProbeRecord{
		{Timestamp: now.Add(-30 * time.Second), SourceRegion: "ru-spb", RelayName: relay, Verdict: "clean", RealityRTTMs: 250, Cover: "www.samsung.com"},
		{Timestamp: now.Add(-20 * time.Second), SourceRegion: "ru-spb", RelayName: relay, Verdict: "unknown"},
	}); err != nil {
		t.Fatalf("insert probes: %v", err)
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		rec := httptest.NewRecorder()
		(&Handlers{analytics: store, pool: pool}).FleetProfiles(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var got fleetProfiles
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Window != "24h" {
			t.Fatalf("window = %q, want 24h by default", got.Window)
		}
		for _, p := range got.Profiles {
			if p.Slug != slug {
				continue
			}
			if p.Box != box || p.Provider != "vultr" || p.Fingerprint != "firefox_148" || p.TrafficUsedGB == nil {
				t.Fatalf("card = %+v", p)
			}
			// The cover comes from the probes, which ClickHouse shows a
			// moment after the insert: read again until they are there.
			if p.Cover != "www.samsung.com" {
				continue
			}
			var rtt, avail bool
			for i, v := range p.RTTMs {
				rtt = rtt || (v != nil && *v == 250)
				avail = avail || (p.AvailPct[i] != nil && *p.AvailPct[i] == 100)
			}
			if rtt && avail {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("served node or its latency never read back: %s", rec.Body.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
}
