package admin

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// The cascade's legs land on the window's buckets: loss per 1000 where
// anything was sent, the round trip where one was heard, null elsewhere, and
// each leg on its own line.
func TestTheCascadesLegsLandOnTheWindowsBuckets(t *testing.T) {
	h := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	starts := []time.Time{h, h.Add(time.Hour), h.Add(2 * time.Hour)}
	rtt := func(v float64) *float64 { return &v }
	cs := []fleetCascade{{Entry: "aezamsk1"}}
	foldCascades(cs, starts, []legBucket{
		{entry: "aezamsk1", layer: "clients", at: h.Unix(), bad: 37, total: 48210, rtt: rtt(18.4)},
		{entry: "aezamsk1", layer: "call", at: h.Unix(), bad: 3, total: 180000, rtt: rtt(44.5)},
		{entry: "aezamsk1", layer: "call", at: h.Add(time.Hour).Unix(), bad: 0, total: 180000},
		{entry: "aezamsk1", layer: "clients", at: h.Add(2 * time.Hour).Unix(), bad: 0, total: 0},
		{entry: "other", layer: "call", at: h.Unix(), bad: 9, total: 9},
		{entry: "aezamsk1", layer: "call", at: h.Add(-time.Hour).Unix(), bad: 9, total: 9},
	})
	c := cs[0]
	if c.ClientLossPm[0] == nil || *c.ClientLossPm[0] < 0.76 || *c.ClientLossPm[0] > 0.77 || *c.ClientRTTMs[0] != 18.4 {
		t.Fatalf("client leg %v %v", c.ClientLossPm[0], c.ClientRTTMs[0])
	}
	if *c.TrunkLossPm[0] != 3*1000.0/180000 || *c.TrunkRTTMs[0] != 44.5 {
		t.Fatalf("trunk leg %v %v", *c.TrunkLossPm[0], *c.TrunkRTTMs[0])
	}
	if c.TrunkLossPm[1] == nil || *c.TrunkLossPm[1] != 0 || c.TrunkRTTMs[1] != nil {
		t.Fatalf("a bucket with nothing lost is 0, with no round trip null: %v %v", c.TrunkLossPm[1], c.TrunkRTTMs[1])
	}
	if c.ClientLossPm[2] != nil || c.ClientLossPm[1] != nil || c.TrunkLossPm[2] != nil {
		t.Fatal("a bucket where nothing was sent is null")
	}
}

// A cascade's speed samples land on the card of the same entry and exit: the
// bucket's median, its round trip, and the week's usual speed; null elsewhere.
func TestTheCascadesSpeedLandsOnItsCard(t *testing.T) {
	h := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	starts := []time.Time{h, h.Add(time.Hour)}
	rtt := func(v float64) *float64 { return &v }
	cs := []fleetCascade{{Entry: "aezamsk1", Exit: "unl1"}, {Entry: "aezamsk1", Exit: "other"}}
	foldCascadeSpeeds(cs, starts, []speedBucket{
		{entry: "aezamsk1", exit: "unl1", at: h.Unix(), mbps: 240, rtt: rtt(185)},
		{entry: "aezamsk1", exit: "unl1", at: h.Add(time.Hour).Unix(), mbps: 250},
		{entry: "aezamsk1", exit: "unl1", at: h.Add(-time.Hour).Unix(), mbps: 9},
		{entry: "nobody", exit: "unl1", at: h.Unix(), mbps: 9},
	}, map[string]float64{"aezamsk1 unl1": 245})
	c := cs[0]
	if c.SpeedMbps[0] == nil || *c.SpeedMbps[0] != 240 || *c.SpeedRTTMs[0] != 185 {
		t.Fatalf("first bucket %v %v", c.SpeedMbps[0], c.SpeedRTTMs[0])
	}
	if c.SpeedMbps[1] == nil || *c.SpeedMbps[1] != 250 || c.SpeedRTTMs[1] != nil {
		t.Fatalf("a sample without a round trip: %v %v", c.SpeedMbps[1], c.SpeedRTTMs[1])
	}
	if c.SpeedNormMbps == nil || *c.SpeedNormMbps != 245 {
		t.Fatalf("norm %v", c.SpeedNormMbps)
	}
	o := cs[1]
	if o.SpeedMbps[0] != nil || o.SpeedNormMbps != nil || len(o.SpeedMbps) != 2 {
		t.Fatalf("another exit's card got this cascade's numbers: %+v", o)
	}
}

// On 19.11 the cascade was measured every 30 minutes from ru-eurodir and its
// card showed no speed: its link names no node, and the card had nothing to
// read. Samples of a key the hub filed under the cascade's pair now reach the
// card; a failed sample, one from a prober outside Russia and one of a key
// filed under no pair do not.
func TestDBTheCascadeCardReadsItsCapacitySamples(t *testing.T) {
	pool := livePostgres(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000)
	entryBox := seedFleetBox(t, pool, "cse"+suffix, "hikkari")
	exitBox := seedFleetBox(t, pool, "csx"+suffix, "hikkari")
	if _, err := pool.Exec(ctx, `INSERT INTO trunk_entries (entry_box, exit_box, forward, members) VALUES ($1, $2, '{"8443": 443}', 32)`, entryBox, exitBox); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM trunk_entries WHERE entry_box = $1`, entryBox)
	})
	ru, lv := "csru-"+suffix, "cslv-"+suffix
	for slot, inRussia := range map[string]bool{ru: true, lv: false} {
		if _, err := pool.Exec(ctx, `INSERT INTO entry_vantages (slot, in_russia) VALUES ($1, $2)`, slot, inRussia); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM entry_vantages WHERE slot IN ($1, $2)`, ru, lv)
	})
	key := func(c byte) string {
		b := make([]byte, 64)
		for i := range b {
			b[i] = c
		}
		return string(b)
	}
	filed, unfiled := key('a'), key('b')
	if _, err := pool.Exec(ctx, `INSERT INTO relay_capacity_cascades (config_key, entry_box, exit_box) VALUES ($1, $2, $3)
		ON CONFLICT (config_key) DO UPDATE SET entry_box = excluded.entry_box, exit_box = excluded.exit_box`, filed, entryBox, exitBox); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	bucket := time.Hour
	start := now.Truncate(bucket)
	at := start.Add(-bucket).Add(time.Minute)
	for i, s := range []struct {
		slot, key, sample string
	}{
		{ru, filed, `{"mbps": 200, "rttMs": 180}`},
		{ru, filed, `{"mbps": 260, "rttMs": 190}`},
		{ru, filed, `{"mbps": 0, "error": "timeout"}`},
		{lv, filed, `{"mbps": 900, "rttMs": 30}`},
		{ru, unfiled, `{"mbps": 900, "rttMs": 30}`},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO relay_capacity_history (slot, config_key, measured_at, sample) VALUES ($1, $2, $3, $4)`,
			s.slot, s.key, at.Add(time.Duration(i)*time.Second), s.sample); err != nil {
			t.Fatal(err)
		}
	}

	cards, err := (&Handlers{pool: pool}).cascades(ctx, []time.Time{start.Add(-bucket), start}, now, bucket)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cards {
		if c.Entry != entryBox {
			continue
		}
		if c.SpeedMbps[1] != nil {
			t.Fatalf("a bucket nothing was measured in has a speed: %v", *c.SpeedMbps[1])
		}
		if c.SpeedMbps[0] == nil || *c.SpeedMbps[0] != 230 || c.SpeedRTTMs[0] == nil || *c.SpeedRTTMs[0] != 185 {
			t.Fatalf("speed %v rtt %v, want the median of the two Russian successes: 230 Mbit/s, 185 ms", c.SpeedMbps[0], c.SpeedRTTMs[0])
		}
		if c.SpeedNormMbps == nil || *c.SpeedNormMbps != 230 {
			t.Fatalf("norm %v, want 230", c.SpeedNormMbps)
		}
		return
	}
	t.Fatalf("no card for %s in %+v", entryBox, cards)
}
