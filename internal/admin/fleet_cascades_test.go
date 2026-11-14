package admin

import (
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
