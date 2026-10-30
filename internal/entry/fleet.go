package entry

// fleet.go is the second reason the hub buys a machine (roadmap 42 §5,
// П9.10, cause short_of_fleet, playbook scale_up).
//
// # Why a second reason
//
// Until this file a machine was bought only to replace a banned one
// (replace_box): a reaction to a death, so for the minutes between the ban and
// the replacement's admission the pool was one machine short — and a wave
// takes several. The owner sets how many live machines each pool should hold,
// and the hub keeps it there: below the target now, or below it by the time a
// machine ordered now could pass admission.
//
// # Why an incident, about a pool
//
// Owner's decision 26.10 (variant A). A shortage is the same shape as
// short_of_ports and short_of_covers — nothing is broken, a spare is missing —
// and like them it is an incident, so `ask` (where anything that spends money
// starts) is the console's queue and its button, a failed attempt is a strike,
// and every attempt is a row in entry_repairs for П9.8. What differs is the
// subject: a shortage belongs to no machine, so the incident names the pool
// (migration 071), and the detector's pass over boxes leaves it alone.
//
// # The forecast
//
// "A spoon is dear when dinner's near": a machine is worth most in the
// minutes nobody had one ready. So beside the shortage of now the hub keeps a
// buffer for the machines it expects to lose before a new one could be ready:
//
//   - λ, the loss rate of this pool at this hoster: machines that died of a
//     ban or of the hoster over the last fleetLossWindow, divided by the hours
//     machines spent carrying people in that window. The estimator that
//     survives censoring — a median of the lifetimes of dead machines alone
//     reads short, because the machines that are still alive are exactly the
//     long ones. ln2/λ is the median life the console can show.
//   - H, the horizon: the owner's, or twice the median from an order to
//     admission measured on this pool's own orders.
//   - the buffer: the median of a Poisson count with mean λ·target·H — the
//     number of losses the pool more likely than not will not exceed before a
//     machine ordered now is ready. Zero until a loss is more likely than not.
//
// With too little history (fewer than fleetMinLosses losses, or no order to
// measure the horizon on and none set) the buffer is zero and the hub keeps
// only the target: a forecast from two data points buys machines on noise.
//
// # The fleet is the one buyer
//
// Since П9.11 a banned machine is not replaced by a repair of its own: it is
// evicted (evict_box) — its relays `suspect`, out of the count of live
// machines — and the shortage that leaves is what buys the replacement, here.
// A suspect machine is not stock (owner's rule): it may come back, but until
// it does it carries nobody. So one ban buys one machine, through one money
// gate and one hourly cap. A machine under an incident that is still being
// cured on the spot (a restart, a reboot) keeps its relays alive and counts.
//
// A machine's service ends for the loss rate when it leaves the pool, not
// when it is finally handed back: the days it waits for its second chance
// carry nobody, and counting them as exposure would read the pool as safer
// than it is.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// CauseShortOfFleet: a pool holds fewer live machines than its target, now or
// by the forecast. Not a failure of any machine — its subject is the pool.
const CauseShortOfFleet = "short_of_fleet"

const (
	// fleetLossWindow is how far back losses are counted for λ. A month: long
	// enough for a pool of a few machines to have a history at all, short
	// enough that last spring's censor rules do not set today's buffer.
	fleetLossWindow = 30 * 24 * time.Hour
	// fleetMinLosses is how many losses λ needs before it is believed.
	fleetMinLosses = 3
	// fleetAdmissionSamples is how many recent orders the horizon's median
	// is taken over: the hoster's speed this month, not last year's.
	fleetAdmissionSamples = 20
)

// fleetLossReasons are the retire reasons that mean the pool lost a machine it
// did not mean to lose. `rotated` and `other` are handbacks the hub or the
// owner chose — an idle machine handed back is not a loss.
var fleetLossReasons = []string{DeathIPBanned, DeathFingerprint, DeathDestination, DeathProviderDown}

// FleetPool names a pool as an incident stores it: the protocol, and for voice
// the region it serves.
func FleetPool(proto, voiceRegion string) string {
	if voiceRegion == "" {
		return proto
	}
	return proto + ":" + voiceRegion
}

// splitPool is FleetPool backwards.
func splitPool(pool string) (proto, voiceRegion string) {
	proto, voiceRegion, _ = strings.Cut(pool, ":")
	return proto, voiceRegion
}

// VoiceRegionSource lists the regions a voice entry can stand in front of:
// the regions with live SFU shards that pin a media address (a voice relay
// forwards only there, provision.go). Declared here, on the consumer side;
// the shard registry implements it (entrywire.VoiceRegions).
type VoiceRegionSource interface {
	VoiceRegions(ctx context.Context) ([]string, error)
}

// SetVoiceRegions wires the shard registry's regions. Unset, a voice pool
// starts with its first machine, which nothing orders.
func (s *Service) SetVoiceRegions(src VoiceRegionSource) { s.voiceRegions = src }

// FleetForecast is one pool's reading: what it holds, what it should, and
// what the hub expects to lose before a machine ordered now is ready.
type FleetForecast struct {
	Pool   string `json:"pool"`
	Target int    `json:"target"`
	// Live is machines in service with at least one relay carrying people,
	// and the ones being emptied for a handback (Leaving of them): until the
	// hoster has them they still carry whoever has not moved, and the hub
	// may take them back (shrink.go) — counting them out would buy a
	// replacement for a machine sold on purpose.
	Live    int `json:"live"`
	Leaving int `json:"leaving"`
	// Losses and ExposureHours are what λ was computed from.
	Losses        int     `json:"losses"`
	ExposureHours float64 `json:"exposureHours"`
	// LossPerBoxDay is λ, per machine per day. Zero with too little history.
	LossPerBoxDay float64 `json:"lossPerBoxDay"`
	// Horizon is how far ahead the buffer looks: twice the median from an
	// order to admission on this pool's own orders.
	Horizon time.Duration `json:"-"`
	// Buffer is how many machines beyond the target the forecast asks for.
	Buffer int `json:"buffer"`
	// Need is how many machines the pool is short by, now and by the
	// forecast together. Zero or less is a pool that is fine.
	Need int `json:"need"`
	// Why is the forecast's own account of itself, in the console's words —
	// including why there is no buffer, when there is none.
	Why string `json:"why"`
	// Target is derived from the pool's load (sizing.go, П9.16); Keep is
	// how many it holds on to before handing one back (П9.17, ≥ Target);
	// TargetWhy says how, and Sizing is what it was read from.
	Keep      int        `json:"keep"`
	TargetWhy string     `json:"targetWhy"`
	Sizing    PoolSizing `json:"sizing"`
}

// Surplus says the pool holds more than it keeps, buffer included: the one
// reading the handback acts on (shrink.go).
func (f FleetForecast) Surplus() bool { return f.Live > f.Keep+f.Buffer }

// HorizonSeconds is the horizon as the console reads it.
func (f FleetForecast) HorizonSeconds() int { return int(f.Horizon / time.Second) }

// fleetForecast reads one pool: its live machines, the target its load asks
// for, and the loss buffer on top.
func (s *Service) fleetForecast(ctx context.Context, policy DetectionPolicy, pool string, sz PoolSizing) (FleetForecast, error) {
	f := FleetForecast{Pool: pool, Sizing: sz}
	proto, region := splitPool(pool)
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE b.handback_since IS NOT NULL) FROM entry_boxes b
		WHERE b.retired_at IS NULL AND b.proto = $1 AND coalesce(b.region, '') = $2
		  AND (b.handback_since IS NOT NULL
		       OR EXISTS (SELECT 1 FROM entry_relays r WHERE r.box = b.name AND r.status = 'alive'))
	`, proto, region).Scan(&f.Live, &f.Leaving); err != nil {
		return f, fmt.Errorf("entry: live machines of %s: %w", pool, err)
	}
	f.Target, f.Keep, f.TargetWhy = loadTarget(sz, f.Live)
	target := f.Target
	f.Need = target - f.Live

	hoster := s.hosterKind()
	if hoster == "" {
		f.Why = "хостера нет — прогноз не по кому считать, держим только цель"
		return f, nil
	}
	now := s.now()

	// λ: losses over the hours machines carried people, both in the window.
	// A machine counts from its admission (its first provisioning that went
	// through), so one born banned and handed back is neither a loss nor
	// exposure — it never carried anybody.
	err := s.pool.QueryRow(ctx, `
		WITH life AS (
			SELECT coalesce((SELECT max(s.since) FROM entry_suspects s
			                 WHERE s.kind = 'box' AND s.subject = b.name AND s.status NOT IN ('returned', 'withdrawn')),
			                b.retired_at) AS retired_at,
			       b.retired_reason,
			       (SELECT min(j.finished_at) FROM entry_jobs j
			        WHERE j.box = b.name AND j.kind = 'provision' AND j.status = 'done') AS admitted
			FROM entry_boxes b
			WHERE b.provider = $1 AND b.proto = $2 AND coalesce(b.region, '') = $3
		)
		SELECT count(*) FILTER (WHERE retired_at >= $4 AND retired_reason = ANY($6)),
		       coalesce(sum(extract(epoch FROM coalesce(retired_at, $5) - greatest(admitted, $4)))
		                FILTER (WHERE coalesce(retired_at, $5) > greatest(admitted, $4)), 0)::float8 / 3600
		FROM life
		WHERE admitted IS NOT NULL AND admitted < $5
	`, hoster, proto, region, now.Add(-fleetLossWindow), now, fleetLossReasons).Scan(&f.Losses, &f.ExposureHours)
	if err != nil {
		return f, fmt.Errorf("entry: losses of %s: %w", pool, err)
	}
	if f.Losses < fleetMinLosses || f.ExposureHours <= 0 {
		f.Why = fmt.Sprintf("потерь за %d дней у %s: %d — мало для прогноза, держим только цель",
			int(fleetLossWindow.Hours()/24), hoster, f.Losses)
		return f, nil
	}
	lossPerHour := float64(f.Losses) / f.ExposureHours
	f.LossPerBoxDay = lossPerHour * 24

	// H: twice the measured median from order to admission.
	f.Horizon, err = s.fleetHorizon(ctx, hoster, proto, region)
	if err != nil {
		return f, fmt.Errorf("entry: order to admission of %s: %w", pool, err)
	}
	if f.Horizon == 0 {
		f.Why = "ни одного заказа хабом, прошедшего допуск, — горизонт не из чего мерить, держим только цель"
		return f, nil
	}

	expected := lossPerHour * float64(target) * f.Horizon.Hours()
	// Never more than the target again: a forecast asking for a pool twice
	// its size is a forecast about a censor that blocks the hoster outright,
	// and buying through that is the wave fuse's question, not the buffer's.
	f.Buffer = min(poissonMedian(expected), target)
	f.Need = target + f.Buffer - f.Live
	f.Why = fmt.Sprintf("темп потерь %.2f машины в сутки на машину (%d за %d дней), горизонт %s: ожидаем потерять %.2f, запас %d",
		f.LossPerBoxDay, f.Losses, int(fleetLossWindow.Hours()/24), f.Horizon.Round(time.Minute), expected, f.Buffer)
	return f, nil
}

// fleetHorizon is how far ahead a pool must be provisioned: twice the median
// from an order to its admission over the pool's own recent orders at this
// hoster. Zero when there is no such order yet.
func (s *Service) fleetHorizon(ctx context.Context, hoster, proto, region string) (time.Duration, error) {
	var median *float64
	var samples int
	err := s.pool.QueryRow(ctx, `
		SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY secs), count(*)
		FROM (
			SELECT extract(epoch FROM j.finished_at - b.created_at)::float8 AS secs
			FROM entry_boxes b
			JOIN LATERAL (SELECT status, finished_at FROM entry_jobs
			              WHERE box = b.name AND kind = 'provision'
			              ORDER BY created_at LIMIT 1) j ON j.status = 'done'
			WHERE b.provider = $1 AND b.proto = $2 AND coalesce(b.region, '') = $3
			  AND b.provider_instance_id IS NOT NULL
			ORDER BY b.created_at DESC
			LIMIT $4
		) t
	`, hoster, proto, region, fleetAdmissionSamples).Scan(&median, &samples)
	if err != nil {
		return 0, err
	}
	if median == nil {
		return 0, nil
	}
	return 2 * time.Duration(*median*float64(time.Second)), nil
}

// poissonMedian is the smallest k with P(N ≤ k) ≥ ½ for N ~ Poisson(mu): the
// number of losses the pool will more likely than not stay within. Zero until
// mu reaches ln 2, which is "a loss before the new machine is ready is more
// likely than not".
func poissonMedian(mu float64) int {
	if mu <= 0 {
		return 0
	}
	pmf := math.Exp(-mu)
	cdf := pmf
	k := 0
	for cdf < 0.5 && k < 1000 {
		k++
		pmf *= mu / float64(k)
		cdf += pmf
	}
	return k
}

// FleetForecasts reads every pool the hub keeps: the two stream protocols,
// which exist whether or not anything carries them yet; a voice pool for
// every region with SFU shards a voice relay can forward to, which exists for
// the same reason — a call in that region needs a voice entry before its
// first machine, not after (П9.19: until then a voice pool started with its
// first machine, and nothing ever ordered the first); every pool with a
// machine on the books; and every pool the fleet pass has kept before
// (entry_fleet_pools) — a wave that retires a pool's last machine, or a
// region whose shards are down for an hour, must not take the pool out of the
// hub's sight with it.
func (s *Service) FleetForecasts(ctx context.Context) ([]FleetForecast, error) {
	policy, err := s.DetectionPolicy(ctx)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{
		FleetPool(ProtoVlessRealityVision, ""): true,
		FleetPool(ProtoVlessXhttp, ""):         true,
	}
	if s.voiceRegions != nil {
		regions, err := s.voiceRegions.VoiceRegions(ctx)
		if err != nil {
			// The shard registry going down costs the voice pools nobody has
			// kept yet, not the fleet: the kept ones come from the table.
			applog.Error(ctx, "entry", "voice regions unreadable", applog.F("err", err.Error()))
		}
		for _, r := range regions {
			want[FleetPool(ProtoHysteria2, r)] = true
		}
	}
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT proto, coalesce(region, '') FROM entry_boxes WHERE retired_at IS NULL
		UNION
		SELECT proto, voice_region FROM entry_fleet_pools
	`)
	if err != nil {
		return nil, fmt.Errorf("entry: pools kept: %w", err)
	}
	for rows.Next() {
		var proto, region string
		if err := rows.Scan(&proto, &region); err != nil {
			rows.Close()
			return nil, fmt.Errorf("entry: scan pool: %w", err)
		}
		want[FleetPool(proto, region)] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("entry: pools kept: %w", err)
	}
	pools := make([]string, 0, len(want))
	for p := range want {
		pools = append(pools, p)
	}
	sort.Strings(pools)
	sizings, err := s.poolSizings(ctx, policy, pools)
	if err != nil {
		return nil, err
	}
	out := make([]FleetForecast, 0, len(pools))
	for _, p := range pools {
		f, err := s.fleetForecast(ctx, policy, p, sizings[p])
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// poolForecast reads one pool, for a repair or a handback that acts on it.
func (s *Service) poolForecast(ctx context.Context, policy DetectionPolicy, pool string) (FleetForecast, error) {
	sizings, err := s.poolSizings(ctx, policy, []string{pool})
	if err != nil {
		return FleetForecast{}, err
	}
	return s.fleetForecast(ctx, policy, pool, sizings[pool])
}

// DetectFleet is the fleet's half of the detector: open short_of_fleet on
// every pool that needs a machine, close it on every pool that no longer
// does. Returns how many it opened and closed.
//
// Like Detect it never repairs. Idempotent per (pool, cause), so it runs on
// the detector's timer and repeats itself freely.
func (s *Service) DetectFleet(ctx context.Context) (opened, closed int, err error) {
	forecasts, err := s.FleetForecasts(ctx)
	if err != nil {
		return 0, 0, err
	}
	need := map[string]FleetForecast{}
	// budget: the pools whose shortage the money cap stops this pass.
	budget := map[string]bool{}
	for _, f := range forecasts {
		need[f.Pool] = f
		// The pool is kept from now on (FleetForecasts).
		proto, region := splitPool(f.Pool)
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO entry_fleet_pools (proto, voice_region) VALUES ($1, $2)
			ON CONFLICT (proto, voice_region) DO NOTHING
		`, proto, region); err != nil {
			applog.Error(ctx, "entry", "fleet pool not kept", applog.F("pool", f.Pool), applog.F("err", err.Error()))
		}
		if f.Need <= 0 {
			continue
		}
		// One more machine has to fit under the money cap, or the shortage
		// is one of money (fleetbudget.go): no repair, a person or a new month.
		fits, why, err := s.machineFits(ctx, f.Sizing.Plan)
		if err != nil {
			applog.Error(ctx, "entry", "fleet budget unreadable", applog.F("pool", f.Pool), applog.F("err", err.Error()))
			continue
		}
		cause, advice, evidence := CauseShortOfFleet, fleetAdvice(f), fleetEvidence(f)
		if !fits {
			cause, advice, evidence = CauseShortOfBudget, budgetAdvice(f, why), budgetEvidence(f, why)
			budget[f.Pool] = true
		}
		inc, err := s.openPool(ctx, f.Pool, cause, advice, evidence)
		if err != nil {
			applog.Error(ctx, "entry", "open fleet incident failed", applog.F("pool", f.Pool), applog.F("err", err.Error()))
			continue
		}
		if inc.OpenedAt.After(s.now().Add(-time.Second)) {
			opened++
			applog.Info(ctx, "entry", "incident opened",
				applog.F("incident", inc.ID.String()), applog.F("pool", f.Pool),
				applog.F("cause", inc.Cause), applog.F("advice", inc.Advice))
		}
	}

	standing, err := s.Incidents(ctx, 1)
	if err != nil {
		return opened, 0, err
	}
	for _, inc := range standing {
		// Acting: the repair says how it ended (repair.go), as on a box. A
		// pool whose machine is being stood up reads short until it is in.
		if inc.Pool == "" || inc.Status != IncidentOpen {
			continue
		}
		f, ok := need[inc.Pool]
		var note string
		switch inc.Cause {
		case CauseShortOfFleet:
			switch {
			case budget[inc.Pool]:
				note = "машину не купить: упёрлись в потолок денег (short_of_budget)"
			case ok && f.Need > 0:
				continue
			case !ok:
				note = "пула больше нет: ни одной машины на учёте"
			default:
				note = "пул добран: живых машин не меньше цели"
			}
		case CauseShortOfBudget:
			if budget[inc.Pool] {
				continue
			}
			note = "место под потолком есть — пул добирает парк"
			if !ok || f.Need <= 0 {
				note = "пулу больше не нужна машина"
			}
		default:
			continue
		}
		if err := s.Close(ctx, inc.ID, IncidentResolved, note, inc.CostCents); err != nil {
			applog.Error(ctx, "entry", "close fleet incident failed", applog.F("incident", inc.ID.String()), applog.F("err", err.Error()))
			continue
		}
		closed++
	}
	// The domain pool's shortage is the same kind of pass (domainpool.go).
	o, c, err := s.detectDomainPool(ctx)
	if err != nil {
		applog.Error(ctx, "entry", "domain pool pass failed", applog.F("err", err.Error()))
	}
	return opened + o, closed + c, nil
}

// fleetAdvice is the one sentence the console shows for a shortage.
func fleetAdvice(f FleetForecast) string {
	if f.Live < f.Target {
		return fmt.Sprintf("в пуле %s живых машин %d из %d — нужна ещё одна", f.Pool, f.Live, f.Target)
	}
	return fmt.Sprintf("в пуле %s живых машин %d при цели %d, но пока новая проходит допуск, ждём потерь: %s — заказ заранее",
		f.Pool, f.Live, f.Target, f.Why)
}

// fleetEvidence is what the shortage was read from, kept as it was.
func fleetEvidence(f FleetForecast) map[string]string {
	ev := map[string]string{
		"live":   strconv.Itoa(f.Live),
		"target": strconv.Itoa(f.Target),
		"buffer": strconv.Itoa(f.Buffer),
		"losses": strconv.Itoa(f.Losses),
	}
	if f.LossPerBoxDay > 0 {
		ev["lossPerBoxDay"] = strconv.FormatFloat(f.LossPerBoxDay, 'f', 3, 64)
	}
	if f.Horizon > 0 {
		ev["horizonSeconds"] = strconv.Itoa(f.HorizonSeconds())
	}
	return ev
}

// scaleUp brings the incident's pool up to what it needs (playbook scale_up):
// every machine it is short of at once, idle ones the hub already pays for
// first, new orders for the rest (obtainMachines). It is also how an evicted
// machine is replaced: the eviction takes it out of the count.
//
// The shortage is read again first. The incident may have stood for an hour
// (an `off` lifted, a pause) while a replacement or a hand order filled the
// pool, and a repair started then must not buy machines the pool no longer
// needs.
//
// Some in and some not is a repair that worked as far as it went: the
// shortage that is left stands, and the next pass reads it again. None in is
// a failed attempt — a strike on this pool (poolRails), so three rounds of
// dead-on-arrival addresses bring a person in rather than a fourth. A
// machine that fails admission — born banned, or never booting — is handed
// back inside obtainMachines.
func (m *Mechanic) scaleUp(ctx context.Context, inc Incident) (repairResult, error) {
	policy, err := m.svc.DetectionPolicy(ctx)
	if err != nil {
		return repairResult{}, err
	}
	f, err := m.svc.poolForecast(ctx, policy, inc.Pool)
	if err != nil {
		return repairResult{}, err
	}
	if f.Need <= 0 {
		return repairResult{outcome: fmt.Sprintf("пул %s уже добран (живых %d, цель %d) — машину не заказывали",
			inc.Pool, f.Live, f.Target)}, nil
	}
	if m.prov == nil {
		return repairResult{}, fmt.Errorf("%w: у хаба нет провизионера, разворачивать нечем", ErrRejected)
	}
	proto, region := splitPool(inc.Pool)
	got, notes := m.prov.obtainMachines(ctx, OrderSpec{Proto: proto, VoiceRegion: region}, f.Need)
	var in, failed []string
	var errs []error
	cost := 0
	for _, g := range got {
		if g.err != nil {
			errs = append(errs, g.err)
			if g.box != "" {
				failed = append(failed, fmt.Sprintf("%s не вошла: %v", g.box, g.err))
			} else {
				failed = append(failed, g.err.Error())
			}
			continue
		}
		rental, err := m.svc.boxRental(ctx, g.box)
		if err != nil {
			return repairResult{}, err
		}
		in = append(in, fmt.Sprintf("%s (%s в месяц)", g.box, money(rental.monthlyCents)))
		cost += rental.monthlyCents
	}
	note := ""
	if len(notes) > 0 {
		note = "; " + strings.Join(notes, "; ")
	}
	if len(in) == 0 {
		return repairResult{}, fmt.Errorf("пулу %s нужно %d, не вошла ни одна: %w%s", inc.Pool, f.Need, errors.Join(errs...), note)
	}
	if len(failed) > 0 {
		note += "; не вышло: " + strings.Join(failed, "; ")
	}
	first, _, _ := strings.Cut(in[0], " ")
	return repairResult{
		outcome: fmt.Sprintf("в пул %s добавлено %d из %d: %s; живых было %d при цели %d%s",
			inc.Pool, len(in), f.Need, strings.Join(in, ", "), f.Live, f.Target, note),
		cost:       cost,
		replacedBy: first,
	}, nil
}
