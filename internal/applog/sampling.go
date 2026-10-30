package applog

import (
	"hash/fnv"
	"math/rand"
	"sync"
)

// Per-category sample rates. Errors and warns are never sampled — they always
// pass. For other levels, rate is in [0, 1]: 1.0 = keep all, 0.0 = drop all.
// When traceID is non-empty the decision is deterministic by hash(traceID) so
// a request that's "in" gets its full chain captured (no half-traces).
//
// Default rate (no override) is 1.0 — keep everything. The map has no writer:
// the setter that filled it went with the contour that called it. Add one back
// together with the first category that actually needs sampling — a knob nobody
// turns reads as "sampling is configured somewhere", and it is not.
var (
	samplingMu    sync.RWMutex
	samplingRates = map[string]float64{}
)

func shouldSample(level, category, traceID string) bool {
	if level == LevelError || level == LevelWarn {
		return true
	}
	samplingMu.RLock()
	rate, ok := samplingRates[category]
	samplingMu.RUnlock()
	if !ok || rate >= 1.0 {
		return true
	}
	if rate <= 0 {
		return false
	}
	if traceID == "" {
		return rand.Float64() < rate
	}
	h := fnv.New32a()
	h.Write([]byte(traceID))
	return float64(h.Sum32()&0xffff)/0x10000 < rate
}
