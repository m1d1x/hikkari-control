package entry

// capacity.go is how much one machine can carry (roadmap 42 П9.15): the
// second half of sizing the fleet by load, beside demand.go (П9.14).
//
// # What "can carry" means
//
// Not the port's line rate and not the hoster's word: the traffic past which
// people start to wait. So the first source is the people themselves — the
// RTT clients report through a machine's relays (entry_reach), read against
// how loaded the machine was at the time. Where that RTT starts to climb is
// the machine's capacity, measured.
//
// RTT needs people to have pushed a machine to its knee, which a young fleet
// never has. So beside it, the machine's own CPU (its relays' pulse): a relay
// is TLS and copying, its CPU grows in step with its traffic, and the line
// through (Mbit/s, CPU) reaches 100 % somewhere — where the box would stop
// keeping up. That is an extrapolation and says so.
//
// Neither, and all that is known is a floor: the most the machine has carried
// without the RTT climbing, or the best a bench run pushed through it. A floor
// is what it can carry at least, never what it can carry.
//
// # Per plan, not per machine
//
// The hub orders a plan at a hoster, not a machine it has met. Every machine
// of a plan reads its own estimate; the plan's capacity is the median of its
// machines' estimates from the best source any of them has, so one machine on
// a noisy neighbour does not set the size of every order.
//
// The plan's monthly traffic allowance is the hoster's word, not a
// measurement, and constrains the month's volume rather than the peak: it
// enters the sizing beside capacity in П9.16, not here.

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

const (
	// capacityWindow is how far back a machine's history is read.
	capacityWindow = 14 * 24 * time.Hour
	// capacityBucket is one reading of a machine's load, as demand's.
	capacityBucket = 5 * time.Minute
	// capacityMinBuckets is how many readings an estimate needs: two hours
	// of a machine actually carrying something.
	capacityMinBuckets = 24
	// kneeMinReports is how many RTT reports a bucket needs before its p95
	// is believed.
	kneeMinReports = 5
	// kneeFactor is how far over its quiet RTT a machine's RTT has to climb
	// to count as people waiting.
	kneeFactor = 1.5
	// kneeMinTail is how many readings at or above a load have to agree
	// that the RTT climbed there: one slow bucket is a slow client.
	kneeMinTail = 3
	// cpuMinFit is how well the CPU line has to explain the readings
	// (R²) before its extrapolation is believed.
	cpuMinFit = 0.5
	// cpuMinSpread is how far apart the lightest and the busiest reading
	// have to be for a line through them to have a slope at all.
	cpuMinSpread = 2.0
)

// Where a capacity came from, best first.
const (
	// CapacityRTT: the load past which people's RTT climbed.
	CapacityRTT = "rtt"
	// CapacityCPU: the load at which the machine's CPU line reaches 100 %.
	CapacityCPU = "cpu"
	// CapacityFloor: nothing measured the limit; what the machine is known to
	// have carried fine.
	CapacityFloor = "floor"
	// CapacityNone: the machine never carried anything to read.
	CapacityNone = "none"
)

// capacityRank orders the sources, best first.
var capacityRank = map[string]int{CapacityRTT: 0, CapacityCPU: 1, CapacityFloor: 2, CapacityNone: 3}

// BoxReading is one machine in one bucket: what it carried, its CPU, and the
// RTT people saw through it (zero reports when nobody said).
type BoxReading struct {
	Mbps   float64
	CPUPct float64
	P95Ms  float64
	N      int
}

// BoxCapacity is one machine's estimate.
type BoxCapacity struct {
	Mbps   float64
	Source string
}

// estimateBoxCapacity reads one machine's history and its best bench run.
func estimateBoxCapacity(readings []BoxReading, benchMbps float64) BoxCapacity {
	loaded := make([]BoxReading, 0, len(readings))
	for _, r := range readings {
		if r.Mbps > 0 {
			loaded = append(loaded, r)
		}
	}
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].Mbps < loaded[j].Mbps })

	var best BoxCapacity
	knee, healthy, kneeOK := rttKnee(loaded)
	if !kneeOK && healthy == 0 && len(loaded) > 0 {
		// Nobody said how the RTT was: what the machine carried is still
		// carried, if not known to have been carried well.
		healthy = loaded[len(loaded)-1].Mbps
	}
	if kneeOK {
		best = BoxCapacity{Mbps: knee, Source: CapacityRTT}
	}
	if cpu, ok := cpuCeiling(loaded); ok && (best.Source == "" || cpu < best.Mbps) {
		best = BoxCapacity{Mbps: cpu, Source: CapacityCPU}
	}
	if best.Source != "" {
		// Whatever the extrapolation says, the machine has already carried
		// `healthy` with people's RTT intact.
		best.Mbps = math.Max(best.Mbps, healthy)
		return best
	}
	if floor := math.Max(healthy, benchMbps); floor > 0 {
		return BoxCapacity{Mbps: floor, Source: CapacityFloor}
	}
	return BoxCapacity{Source: CapacityNone}
}

// rttKnee finds the lightest load from which people's RTT has climbed past
// kneeFactor × the machine's quiet RTT (the median of its lightest third):
// the first run of kneeMinTail readings, neighbours by load, whose median is
// over the limit — so one slow bucket is a slow client, not a knee. `loaded`
// is sorted by load. Returns the knee, the heaviest load seen with the RTT
// intact (the healthy floor), and whether a knee was found. With too few
// judged readings there is neither.
func rttKnee(loaded []BoxReading) (knee, healthy float64, ok bool) {
	judged := make([]BoxReading, 0, len(loaded))
	for _, r := range loaded {
		if r.N >= kneeMinReports {
			judged = append(judged, r)
		}
	}
	if len(judged) < capacityMinBuckets {
		return 0, 0, false
	}
	quiet := judged[:len(judged)/3]
	q := make([]float64, len(quiet))
	for i, r := range quiet {
		q[i] = r.P95Ms
	}
	limit := median(q) * kneeFactor
	for i := 0; i+kneeMinTail <= len(judged); i++ {
		w := make([]float64, kneeMinTail)
		for j := range w {
			w[j] = judged[i+j].P95Ms
		}
		if median(w) > limit {
			return judged[i].Mbps, healthyBelow(judged[:i], limit), true
		}
	}
	return 0, healthyBelow(judged, limit), false
}

// healthyBelow is the heaviest reading whose RTT stayed under the limit.
func healthyBelow(judged []BoxReading, limit float64) float64 {
	var h float64
	for _, r := range judged {
		if r.P95Ms <= limit {
			h = math.Max(h, r.Mbps)
		}
	}
	return h
}

// cpuCeiling extrapolates the line through (Mbit/s, CPU %) to 100 % CPU. Not
// believed with too few readings, too little spread between the lightest and
// the busiest to have a slope, a slope that does not rise, or a line that
// explains less than cpuMinFit of the CPU.
func cpuCeiling(loaded []BoxReading) (float64, bool) {
	var xs, ys []float64
	for _, r := range loaded {
		if r.CPUPct > 0 {
			xs, ys = append(xs, r.Mbps), append(ys, r.CPUPct)
		}
	}
	if len(xs) < capacityMinBuckets {
		return 0, false
	}
	lo, hi := xs[0], xs[0]
	for _, x := range xs {
		lo, hi = math.Min(lo, x), math.Max(hi, x)
	}
	if hi < lo*cpuMinSpread {
		return 0, false
	}
	a, b, r2 := linearFit(xs, ys)
	if b <= 0 || r2 < cpuMinFit {
		return 0, false
	}
	return (100 - a) / b, true
}

// linearFit is the least-squares line y = a + b·x and its R².
func linearFit(xs, ys []float64) (a, b, r2 float64) {
	n := float64(len(xs))
	var sx, sy, sxx, sxy, syy float64
	for i := range xs {
		x, y := xs[i], ys[i]
		sx, sy, sxx, sxy, syy = sx+x, sy+y, sxx+x*x, sxy+x*y, syy+y*y
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0, 0, 0
	}
	b = (n*sxy - sx*sy) / den
	a = (sy - b*sx) / n
	vy := n*syy - sy*sy
	if vy == 0 {
		return a, b, 0
	}
	r := (n*sxy - sx*sy) / math.Sqrt(den*vy)
	return a, b, r * r
}

// median of a non-empty slice; the slice is sorted in place.
func median(v []float64) float64 {
	sort.Float64s(v)
	m := len(v) / 2
	if len(v)%2 == 1 {
		return v[m]
	}
	return (v[m-1] + v[m]) / 2
}

// PlanCapacity is how much one plan of one hoster carries, for the console
// now and for the fleet's sizing in П9.16.
type PlanCapacity struct {
	Provider string  `json:"provider"`
	Plan     string  `json:"plan"`
	Mbps     float64 `json:"mbps"`
	Source   string  `json:"source"`
	// Boxes is how many machines the figure is the median of; Machines how
	// many of the plan were read at all.
	Boxes    int    `json:"boxes"`
	Machines int    `json:"machines"`
	Why      string `json:"why"`
}

// planCapacity folds a plan's machines into one figure: the median of the
// machines whose source ranks best.
func planCapacity(provider, plan string, boxes []BoxCapacity) PlanCapacity {
	pc := PlanCapacity{Provider: provider, Plan: plan, Source: CapacityNone, Machines: len(boxes)}
	for _, b := range boxes {
		if capacityRank[b.Source] < capacityRank[pc.Source] {
			pc.Source = b.Source
		}
	}
	var v []float64
	for _, b := range boxes {
		if b.Source == pc.Source && b.Source != CapacityNone {
			v = append(v, b.Mbps)
		}
	}
	pc.Boxes = len(v)
	if len(v) > 0 {
		pc.Mbps = median(v)
	}
	switch pc.Source {
	case CapacityRTT:
		pc.Why = fmt.Sprintf("мерено по RTT людей: дальше %.0f Мбит/с задержка растёт (медиана %d машин)", pc.Mbps, pc.Boxes)
	case CapacityCPU:
		pc.Why = fmt.Sprintf("по линии CPU: к 100 %% машина выйдет на %.0f Мбит/с (медиана %d машин, до колена RTT ещё не доходили)", pc.Mbps, pc.Boxes)
	case CapacityFloor:
		pc.Why = fmt.Sprintf("предел не мерен: держит минимум %.0f Мбит/с — столько уже возила или выдал бенч", pc.Mbps)
	default:
		pc.Why = "машины тарифа ничего не возили — ёмкость не из чего читать"
	}
	return pc
}

// PlanCapacities reads every plan the hub's machines were ordered on. A
// machine rented by hand has no plan the hub could order again and is left
// out. Nil, without an error, when the hub has no dataset to read.
func (s *Service) PlanCapacities(ctx context.Context) ([]PlanCapacity, error) {
	if s.load == nil {
		return nil, nil
	}
	relays, err := s.fleetRelays(ctx)
	if err != nil {
		return nil, err
	}
	type planKey struct{ provider, plan string }
	planOf := map[string]planKey{}
	groups := map[string]string{}
	for _, r := range relays {
		if r.plan == "" {
			continue
		}
		planOf[r.box] = planKey{r.provider, r.plan}
		groups[r.name] = r.box
	}
	since := s.now().Add(-capacityWindow)
	loads, err := s.load.Load(ctx, since, capacityBucket, groups)
	if err != nil {
		return nil, fmt.Errorf("entry: machine load: %w", err)
	}
	rtts, err := s.load.ReachRTT(ctx, since, capacityBucket, groups)
	if err != nil {
		return nil, fmt.Errorf("entry: machine rtt: %w", err)
	}
	benches, err := s.load.BenchPeaks(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("entry: bench peaks: %w", err)
	}

	type at struct {
		box string
		t   time.Time
	}
	rtt := map[at]RTTPoint{}
	for _, r := range rtts {
		rtt[at{r.Key, r.At}] = r
	}
	readings := map[string][]BoxReading{}
	for _, l := range loads {
		r := BoxReading{Mbps: l.Mbps, CPUPct: l.CPUPct}
		if x, ok := rtt[at{l.Key, l.At}]; ok {
			r.P95Ms, r.N = x.P95Ms, x.N
		}
		readings[l.Key] = append(readings[l.Key], r)
	}

	byPlan := map[planKey][]BoxCapacity{}
	for box, k := range planOf {
		byPlan[k] = append(byPlan[k], estimateBoxCapacity(readings[box], benches[box]))
	}
	out := make([]PlanCapacity, 0, len(byPlan))
	for k, boxes := range byPlan {
		out = append(out, planCapacity(k.provider, k.plan, boxes))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Plan < out[j].Plan
	})
	return out, nil
}
