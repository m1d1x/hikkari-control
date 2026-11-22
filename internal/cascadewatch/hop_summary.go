package cascadewatch

import (
	"slices"
	"time"

	"github.com/m1d1x/hikkari-control/internal/entry"
	"github.com/m1d1x/hikkari-control/internal/hoptrace"
)

type hopCoverage struct {
	Seconds int    `json:"seconds"`
	Missing int    `json:"missing"`
	Drops   uint64 `json:"capture_drops"`
	Capped  int    `json:"capped_seconds"`
}

type hopObservation struct {
	box, peer string
	out       bool
}

type hopPacketID struct {
	id   uint64
	port uint16
}

type hopCount struct {
	Sent    int    `json:"seen_at_start"`
	Matched int    `json:"also_seen_at_end"`
	P50MS   *int64 `json:"timestamp_delta_p50_ms,omitempty"`
	MaxMS   *int64 `json:"timestamp_delta_max_ms,omitempty"`
}

type hopSegment struct {
	From     string   `json:"from"`
	To       string   `json:"to"`
	Baseline hopCount `json:"before"`
	Gap      hopCount `json:"during"`
	Reliable bool     `json:"complete_capture"`
}

type hopPoint struct {
	Point string       `json:"point"`
	Up    []hopSegment `json:"up"`
	Down  []hopSegment `json:"down"`
}

func summarizeHops(p entry.TrunkPair, incident hopIncident, windows map[string]hoptrace.Window, configs map[string]hoptrace.Config) map[string]any {
	from, until := incident.captureWindow()
	coverage := make(map[string]hopCoverage)
	var samplingBits uint8
	for box, config := range configs {
		c := hopCoverage{Missing: int(until - from)}
		window := windows[box]
		if window.Config == config.Encode() {
			for _, b := range window.Buckets {
				samplingBits = max(samplingBits, b.SamplingBits)
				if b.Complete {
					c.Seconds++
					c.Missing--
				}
				c.Drops += uint64(b.Drops)
				if b.Overflow {
					c.Capped++
				}
			}
		} else {
			delete(windows, box)
		}
		coverage[box] = c
	}
	observed := make(map[hopObservation]map[hopPacketID]int64)
	packetTimes := func(observation hopObservation) map[hopPacketID]int64 {
		if packets, ok := observed[observation]; ok {
			return packets
		}
		packets := make(map[hopPacketID]int64)
		for _, b := range windows[observation.box].Buckets {
			for _, sample := range b.Packets {
				peers := configs[observation.box].Peers
				if !hoptrace.Sampled(sample.ID, samplingBits) {
					continue
				}
				if sample.Out == observation.out && int(sample.Peer) < len(peers) && peers[sample.Peer].Name == observation.peer {
					id := hopPacketID{id: sample.ID, port: sample.Port}
					if _, exists := packets[id]; !exists {
						packets[id] = sample.At
					}
				}
			}
		}
		observed[observation] = packets
		return packets
	}
	complete := func(box string) bool {
		c := coverage[box]
		return c.Missing == 0 && c.Drops == 0 && c.Capped == 0
	}
	segments := func(path []hopObservation) []hopSegment {
		out := make([]hopSegment, 0, len(path)-1)
		for i := 1; i < len(path); i++ {
			a, b := path[i-1], path[i]
			src, dst := packetTimes(a), packetTimes(b)
			out = append(out, hopSegment{
				From: a.box, To: b.box, Reliable: complete(a.box) && complete(b.box),
				Baseline: countHops(src, dst, incident.at.Add(-2*time.Second), incident.at),
				Gap:      countHops(src, dst, incident.at, incident.end()),
			})
		}
		return out
	}
	points := make([]hopPoint, 0, len(p.Points))
	for _, point := range p.Points {
		points = append(points, hopPoint{
			Point: point,
			Up: segments([]hopObservation{
				{p.Entry, point, true}, {point, p.Entry, false}, {point, p.Exit, true}, {p.Exit, point, false},
			}),
			Down: segments([]hopObservation{
				{p.Exit, point, true}, {point, p.Exit, false}, {point, p.Entry, true}, {p.Entry, point, false},
			}),
		})
	}
	return map[string]any{
		"kind": "hop_trace", "gap_at": incident.at, "gap_ms": incident.gap.Milliseconds(),
		"sample_one_in": 1 << samplingBits,
		"analyzed_ms":   incident.end().Sub(incident.at).Milliseconds(), "coverage": coverage, "points": points,
	}
}

func countHops(src, dst map[hopPacketID]int64, from, until time.Time) hopCount {
	var count hopCount
	var deltas []int64
	for id, at := range src {
		if at < from.UnixMilli() || at >= until.UnixMilli() {
			continue
		}
		count.Sent++
		if end, ok := dst[id]; ok {
			deltas = append(deltas, end-at)
		}
	}
	count.Matched = len(deltas)
	if len(deltas) > 0 {
		slices.Sort(deltas)
		count.P50MS, count.MaxMS = &deltas[len(deltas)/2], &deltas[len(deltas)-1]
	}
	return count
}
