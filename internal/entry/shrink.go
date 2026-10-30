package entry

// shrink.go is how a pool gives back a machine it no longer needs (roadmap 42
// П9.17): the other half of sizing by load, beside scale_up (fleet.go).
//
// # When
//
// A pool is over what it needs when it holds more than `keep` (sizing.go) plus
// the loss buffer: the forecast still fits under fleetShrinkHeadroom without
// the machine. Then the hub waits for the last handbackWindow of the hour the
// hoster has already charged for — a machine handed back at minute five of its
// hour is fifty-five minutes paid for nothing — and lets it go.
//
// There is no hold on top (П9.19, owner's decision 28.10). The hold guessed
// whether a surge would come back; the forecast is what answers that — `keep`
// is the busiest the pool is expected to be until a machine ordered now could
// be admitted (demand.go) — and a machine bought again after an hour costs one
// admission, not a second month: rent is hourly. Holding ten idle machines a
// day "in case" is ten machine-days paid for nothing.
//
// # How many
//
// All of the surplus, each machine at the end of its own paid hour: a pool
// that held ten machines more than it needs gives back ten, not one per
// pass.
//
// # How
//
// Not by killing its relays, which cuts everyone on them. The machine is
// emptied first, the way a rollout empties a box (Service.Drain): its relays
// leave the pool as `draining`, every holder refetches its batch, and a client
// moves its session onto the replacement before letting go of the old one.
// The machine is handed back on a later pass, once nobody is left on its
// relay ports (counted over ssh, as a rollout counts) or handbackDrainFor has
// passed — the same deadline a rollout gives. The mechanic is never held
// waiting: each pass moves every machine one step.
//
// While it is leaving, the machine still counts as live (FleetForecast.Live)
// — it carries whoever has not moved, and the hub may want it back. If the
// pool stops being over what it needs before the machines are gone, as many
// as it needs again have their relays restored, the last to start leaving
// first, instead of new machines being bought.
//
// # Which
//
// The least loaded machine of the pool over the last quarter of an hour: the
// fewest people to move. Without a dataset to read it from, the oldest.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/ssh"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

const (
	// handbackWindow is how much of the paid hour may be left for a drain
	// to start: enough for handbackDrainFor and a few mechanic passes.
	handbackWindow = 15 * time.Minute
	// handbackDrainFor is how long holders get to move off a leaving machine
	// before it is handed back with whoever is left — a rollout's drainFor.
	handbackDrainFor = 5 * time.Minute
	// handbackLoadWindow is how far back "least loaded" looks.
	handbackLoadWindow = 15 * time.Minute
)

// paidHourLeft is how much of the hour the hoster has charged for is left on
// a machine created at `created`: hourly rent is charged in whole hours from
// creation.
func paidHourLeft(created, now time.Time) time.Duration {
	el := now.Sub(created)
	if el < 0 {
		return time.Hour
	}
	return time.Hour - el%time.Hour
}

// handBackSurplus moves every surplus machine one step: the machines already
// leaving first (handed back, or taken back), then a drain on every machine
// its pool no longer needs that is at the end of its paid hour. Returns the
// machines handed back. An optional name list restricts both drain and deletion
// to those machines; omitted, the ordinary pass covers the whole fleet.
func (p *Provisioner) handBackSurplus(ctx context.Context, only ...string) ([]string, error) {
	if _, err := p.svc.hoster(); err != nil {
		return nil, nil
	}
	policy, err := p.svc.DetectionPolicy(ctx)
	if err != nil {
		return nil, err
	}
	gone, err := p.finishHandbacks(ctx, policy, only...)
	if err != nil {
		return gone, err
	}
	return gone, p.startHandbacks(ctx, policy, only...)
}

// leavingBox is a machine being emptied for a handback.
type leavingBox struct {
	name, proto, region string
	since               time.Time
}

// finishHandbacks hands back every leaving machine that is empty or past its
// drain, and takes back the ones their pool needs again. Returns the machines
// handed back.
func (p *Provisioner) finishHandbacks(ctx context.Context, policy DetectionPolicy, only ...string) ([]string, error) {
	svc := p.svc
	rows, err := svc.pool.Query(ctx, `
		SELECT name, proto, coalesce(region, ''), handback_since FROM entry_boxes
		WHERE retired_at IS NULL AND handback_since IS NOT NULL
		  AND ($1::text[] IS NULL OR name = ANY($1))
		ORDER BY handback_since, name
	`, only)
	if err != nil {
		return nil, fmt.Errorf("entry: leaving machines: %w", err)
	}
	boxes, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (leavingBox, error) {
		var b leavingBox
		err := r.Scan(&b.name, &b.proto, &b.region, &b.since)
		return b, err
	})
	if err != nil {
		return nil, fmt.Errorf("entry: leaving machines: %w", err)
	}
	byPool := map[string][]leavingBox{}
	var pools []string
	for _, b := range boxes {
		key := FleetPool(b.proto, b.region)
		if _, seen := byPool[key]; !seen {
			pools = append(pools, key)
		}
		byPool[key] = append(byPool[key], b)
	}
	var gone []string
	for _, pool := range pools {
		f, err := svc.poolForecast(ctx, policy, pool)
		if err != nil {
			return gone, err
		}
		leaving := byPool[pool]
		// How many of them the pool can do without: a leaving machine
		// still counts as live, so the surplus is read over all of them.
		over := max(0, f.Live-f.Keep-f.Buffer)
		if back := len(leaving) - over; back > 0 {
			// The last to start leaving go back first: they have moved the
			// fewest people off.
			for _, b := range leaving[len(leaving)-back:] {
				if err := svc.takeBack(ctx, b.name); err != nil {
					return gone, err
				}
				applog.Info(ctx, "entry", "leaving machine taken back", applog.F("box", b.name), applog.F("pool", pool),
					applog.F("why", f.TargetWhy))
			}
			leaving = leaving[:len(leaving)-back]
		}
		for _, b := range leaving {
			done, err := p.finishHandback(ctx, f, b)
			if err != nil {
				return gone, err
			}
			if done {
				gone = append(gone, b.name)
			}
		}
	}
	return gone, nil
}

// finishHandback hands back one leaving machine if nobody is left on it or its
// drain is over; done is false when it is still being emptied, or when the
// operator took it meanwhile (it is theirs then, back in the pool).
func (p *Provisioner) finishHandback(ctx context.Context, f FleetForecast, b leavingBox) (done bool, err error) {
	svc := p.svc
	conns, err := p.boxConnections(ctx, b.name)
	if err != nil {
		// Unknown is not empty: the deadline decides.
		applog.Error(ctx, "entry", "leaving machine count failed", applog.F("box", b.name), applog.F("err", err.Error()))
		conns = -1
	}
	drained := svc.now().Sub(b.since) >= handbackDrainFor
	if conns != 0 && !drained {
		return false, nil
	}
	left := "никого"
	if conns != 0 {
		left = fmt.Sprintf("срок %s вышел, на релеях ещё %s", handbackDrainFor, connWord(conns))
	}
	err = p.execJob(ctx, JobRetire, b.name, uuid.Nil, func(j *jobRun) error {
		j.step("retire")
		j.log("пул %s выше нужного (живых %d, держать %d и запас %d), машину освободили: %s — сдаю",
			f.Pool, f.Live, f.Keep, f.Buffer, left)
		return j.autoHandBack(DeathRotated)
	})
	if errors.Is(err, errOperatorsMachine) {
		return false, svc.takeBack(ctx, b.name)
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func connWord(n int) string {
	if n < 0 {
		return "неизвестно сколько соединений"
	}
	return strconv.Itoa(n) + " соединений"
}

// handbackCandidate is a machine a surplus pool could give back.
type handbackCandidate struct {
	name    string
	created time.Time
	mbps    float64
}

// startHandbacks begins emptying, in every pool over what it needs, as many
// machines as it is over by — of those in the last minutes of their paid
// hour, the least loaded first. A machine not at the end of its hour yet is a
// later pass's: its hour is paid either way.
func (p *Provisioner) startHandbacks(ctx context.Context, policy DetectionPolicy, only ...string) error {
	svc := p.svc
	h, err := svc.hoster()
	if err != nil {
		return nil
	}
	forecasts, err := svc.FleetForecasts(ctx)
	if err != nil {
		return err
	}
	now := svc.now()
	for _, f := range forecasts {
		// Leaving machines are surplus already being given back.
		over := f.Live - f.Keep - f.Buffer - f.Leaving
		if over <= 0 {
			continue
		}
		proto, region := splitPool(f.Pool)
		rows, err := svc.pool.Query(ctx, `
			SELECT b.name, b.created_at FROM entry_boxes b
			WHERE b.retired_at IS NULL AND b.proto = $1 AND coalesce(b.region, '') = $2
			  AND b.provider = $3 AND b.provider_instance_id IS NOT NULL AND b.handback_since IS NULL
			  AND ($4::text[] IS NULL OR b.name = ANY($4))
			  AND EXISTS (SELECT 1 FROM entry_relays r WHERE r.box = b.name AND r.status = 'alive')
			  AND NOT EXISTS (SELECT 1 FROM entry_relays r WHERE r.box = b.name AND r.status IN ('testing', 'draining'))
			  AND NOT EXISTS (SELECT 1 FROM entry_incidents i WHERE i.box = b.name AND i.status IN ('open', 'acting'))
			  AND NOT EXISTS (SELECT 1 FROM entry_repairs r WHERE r.box = b.name AND r.status = 'running')
			  AND NOT EXISTS (SELECT 1 FROM entry_jobs j WHERE j.box = b.name AND j.status = 'running')
			  AND NOT `+boxInOperatorsHands+`
			ORDER BY b.created_at, b.name
		`, proto, region, h.kind, only)
		if err != nil {
			return fmt.Errorf("entry: surplus machines of %s: %w", f.Pool, err)
		}
		cands, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (handbackCandidate, error) {
			var c handbackCandidate
			err := r.Scan(&c.name, &c.created)
			return c, err
		})
		if err != nil {
			return fmt.Errorf("entry: surplus machines of %s: %w", f.Pool, err)
		}
		inWindow := cands[:0]
		for _, c := range cands {
			if paidHourLeft(c.created, now) <= handbackWindow {
				inWindow = append(inWindow, c)
			}
		}
		for _, box := range p.leastLoaded(ctx, inWindow, over) {
			relays, holders, err := svc.drainForHandback(ctx, box.name)
			if errors.Is(err, ErrBoxRetired) || errors.Is(err, ErrNoSuchBox) {
				continue
			}
			if err != nil {
				return err
			}
			applog.Info(ctx, "entry", "surplus machine leaving", applog.F("box", box.name), applog.F("pool", f.Pool),
				applog.F("relays", relays), applog.F("holders", holders), applog.F("mbps", box.mbps),
				applog.F("paid_left", paidHourLeft(box.created, now).Round(time.Minute).String()))
		}
	}
	return nil
}

// leastLoaded is the n candidates that carried least over handbackLoadWindow;
// of equals — and all are equal when the dataset says nothing — the oldest.
func (p *Provisioner) leastLoaded(ctx context.Context, cands []handbackCandidate, n int) []handbackCandidate {
	if len(cands) == 0 || n <= 0 {
		return nil
	}
	if loads, err := p.svc.boxLoads(ctx, cands); err != nil {
		applog.Error(ctx, "entry", "handback load read failed", applog.F("err", err.Error()))
	} else {
		for i := range cands {
			cands[i].mbps = loads[cands[i].name]
		}
	}
	return pickLeastLoaded(cands, n)
}

// pickLeastLoaded is the n candidates with the least traffic, the oldest of
// equals first (candidates come oldest first).
func pickLeastLoaded(cands []handbackCandidate, n int) []handbackCandidate {
	sorted := append([]handbackCandidate(nil), cands...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].mbps < sorted[j].mbps })
	return sorted[:min(n, len(sorted))]
}

// boxLoads is each candidate's mean traffic over handbackLoadWindow, in
// Mbit/s; empty without a dataset.
func (s *Service) boxLoads(ctx context.Context, cands []handbackCandidate) (map[string]float64, error) {
	out := map[string]float64{}
	if s.load == nil {
		return out, nil
	}
	want := map[string]bool{}
	for _, c := range cands {
		want[c.name] = true
	}
	relays, err := s.fleetRelays(ctx)
	if err != nil {
		return nil, err
	}
	groups := map[string]string{}
	for _, r := range relays {
		if want[r.box] {
			groups[r.name] = r.box
		}
	}
	points, err := s.load.Load(ctx, s.now().Add(-handbackLoadWindow), demandBucket, groups)
	if err != nil {
		return nil, err
	}
	n := map[string]int{}
	for _, pt := range points {
		out[pt.Key] += pt.Mbps
		n[pt.Key]++
	}
	for k := range out {
		out[k] /= float64(n[k])
	}
	return out, nil
}

// drainForHandback takes the box's alive relays out of the pool as a
// rollout's drain does (Service.Drain) and marks it leaving, in one
// transaction: a machine never counts as leaving with its relays still handed
// out, nor drained without anyone meaning to hand it back.
func (s *Service) drainForHandback(ctx context.Context, box string) (relays, holders int, err error) {
	return s.drain(ctx, box, StatusDraining, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE entry_boxes SET handback_since = $2 WHERE name = $1`, box, s.now())
		return err
	})
}

// takeBack returns a leaving machine to its pool: its drained relays alive
// again and the mark cleared, in one transaction — a machine with its relays
// back and the mark still on would be handed back with people on it, and one
// with the mark off and its relays still drained would be carried by nobody.
func (s *Service) takeBack(ctx context.Context, box string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if _, err := tx.Exec(ctx, restoreBoxRelays, box); err != nil {
		return fmt.Errorf("entry: take back %s: %w", box, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE entry_boxes SET handback_since = NULL WHERE name = $1`, box); err != nil {
		return fmt.Errorf("entry: take back %s: %w", box, err)
	}
	return tx.Commit(ctx)
}

// boxConnections counts the connections established on the box's relay ports
// right now, over ssh, as a rollout's drain does (connCountScript). A voice
// box rides one unconnected UDP socket and has nothing to count: zero.
func (p *Provisioner) boxConnections(ctx context.Context, box string) (int, error) {
	acc, err := p.svc.boxAccess(ctx, box)
	if err != nil {
		return 0, err
	}
	if acc.voice() {
		return 0, nil
	}
	if acc.hostKey == "" {
		return 0, fmt.Errorf("%w: ключ хоста %s не записан", ErrRejected, box)
	}
	relays, err := p.svc.relayIdentities(ctx, box)
	if err != nil {
		return 0, err
	}
	if len(relays) == 0 {
		return 0, nil
	}
	ports := make([]string, 0, len(relays))
	for _, rel := range relays {
		ports = append(ports, strconv.Itoa(rel.id.Port))
	}
	sort.Strings(ports)
	hubKey, _, err := p.svc.HubSSHKey(ctx)
	if err != nil {
		return 0, fmt.Errorf("ssh-ключ хаба: %w", err)
	}
	r, err := p.dialBox(ctx, box, acc, ssh.PublicKeys(hubKey))
	if err != nil {
		return 0, err
	}
	defer r.Close()
	out, err := r.Run(ctx, "bash -s -- "+strings.Join(ports, " "), strings.NewReader(connCountScript))
	if err != nil {
		return 0, fmt.Errorf("счёт соединений: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("счёт соединений: ответ %s", remoteWord(out))
	}
	return n, nil
}
