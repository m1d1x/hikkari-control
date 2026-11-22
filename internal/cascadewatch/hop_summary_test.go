package cascadewatch

import (
	"testing"
	"time"

	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/hoptrace"
)

func TestHopTraceIdentifiesLossOnTheEntrySideWithoutBlamingAnIncompleteCapture(t *testing.T) {
	p := entry.TrunkPair{Entry: "in", EntryAddress: "192.0.2.1", Exit: "out", ExitAddress: "192.0.2.4",
		Points: []string{"point-a", "point-b"}, HopPorts: []uint16{28461},
		PointNames: map[string]string{"192.0.2.2": "point-a", "192.0.2.3": "point-b"}}
	configs, err := hopConfigs(p)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(12000, 0)
	incident := hopIncident{minute: at, at: at, gap: 2 * time.Second}
	windows := make(map[string]hoptrace.Window)
	from, until := incident.captureWindow()
	for box, config := range configs {
		w := hoptrace.Window{Config: config.Encode()}
		for sec := from; sec < until; sec++ {
			w.Buckets = append(w.Buckets, hoptrace.Bucket{Second: sec, Complete: true})
		}
		windows[box] = w
	}
	add := func(box string, peer uint8, out bool, id uint64, instant time.Time) {
		w := windows[box]
		b := &w.Buckets[instant.Unix()-from]
		b.Packets = append(b.Packets, hoptrace.Packet{At: instant.UnixMilli(), ID: id, Port: 28461, Peer: peer, Out: out})
	}
	before := at.Add(-time.Second)
	add("in", 0, true, 41, before)
	add("point-a", 0, false, 41, before)
	add("point-a", 1, true, 41, before)
	add("out", 0, false, 41, before)
	add("in", 0, true, 42, at.Add(time.Second))
	add("in", 1, true, 43, at.Add(time.Second))
	windows["point-b"].Buckets[4].Drops = 1
	points := summarizeHops(p, incident, windows, configs)["points"].([]hopPoint)
	a, b := points[0].Up[0], points[1].Up[0]
	if a.Baseline.Sent != 1 || a.Baseline.Matched != 1 || a.Gap.Sent != 1 || a.Gap.Matched != 0 || !a.Reliable {
		t.Fatalf("complete first leg: %+v", a)
	}
	if b.Gap.Sent != 1 || b.Gap.Matched != 0 || b.Reliable {
		t.Fatalf("incomplete first leg: %+v", b)
	}
	if points[0].Up[1].Baseline.Matched != 1 || points[0].Up[2].Baseline.Matched != 1 {
		t.Fatalf("baseline should match through both NAT boundaries: %+v", points[0].Up)
	}
}
