package entry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/ssh"

	"github.com/m1d1x/hikkari-control/internal/applog"
)

// Rolling out a new relay build (roadmap 42 П6.14 Ф4, the owner's scheme of
// 19.10). The hub's image carries nekko-edge; a hub that starts from a newer
// image finds boxes running another build and moves them onto its own, push
// from the hub over the ssh it already holds:
//
//  1. Detect: per architecture, a live box whose edge_sha differs from the
//     image's. A box never looked at (edge_sha NULL) is read over ssh first.
//  2. Canary: ONE box, the newest without probe failures — if it dies, it is
//     the code, not an old address. Its relays go out of the pool (testing),
//     holders move off, a bench on the old build, the swap, an admission
//     probe, a bench on the new build, CompareBench.
//  3. Pass: back to the pool, the rollout goes on. Fail: the old build is put
//     back (it waits on the box as nekko-edge.prev), the relays return, the
//     rollout stops as failed — and stays failed: a build is tried once.
//  4. The rest, one box at a time, emptiest first: drained, swapped, probed,
//     back in the pool. One at a time even when nobody is on them: the pool
//     never has more than one box out.
//
// A hub restarted mid-rollout picks up where it can: a canary cut off is
// rolled back and tried again (a stack deploy is no verdict on the build), a
// box cut off mid-update is updated again, and a rollout of a build this
// image no longer carries is stopped.

// Rollout stages (entry_rollouts.stage).
const (
	RolloutCanary  = "canary"
	RolloutRolling = "rolling"
	RolloutDone    = "done"
	RolloutFailed  = "failed"
	RolloutStopped = "stopped"
)

// RelayHealth says when the routine probes last saw each relay fail — the
// canary must not be a box that is already sick (consumer side: the
// ClickHouse entry_probes dataset in production). Relays with no failure since
// `since` are absent.
//
// Both read only the rows of `slots`, the active observation slots: the
// registry is the allowlist here as it is for the detector (audit 42 recheck
// НН3) — a retired slot's rows, or a region nobody registered, neither hold a
// box out of the canary nor bend the path check.
type RelayHealth interface {
	FailingRelays(ctx context.Context, since time.Time, slots []string) (map[string]time.Time, error)
	// RelayRTT is the median tunnel RTT of the relay's successful probes in
	// [from, to), and how many there were: the path's usual level, and its
	// level around a lone canary's benches.
	RelayRTT(ctx context.Context, relay string, from, to time.Time, slots []string) (median float64, n int, err error)
}

// activeSlots is the allowlist RelayHealth reads under, sorted.
func (o *Orchestrator) activeSlots(ctx context.Context) ([]string, error) {
	set, err := o.svc.activeVantageSet(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}

// A lone canary's path counts as steady while its probe RTT stays within
// this share of its daily median (owner's decision 19.10).
const (
	pathRTTDrift     = 0.3
	pathRTTBaseline  = 24 * time.Hour
	pathRTTRecent    = 30 * time.Minute
	pathRTTMinSample = 20
	// The floor on the RECENT window, which is minutes long and so can never
	// hold as many probes as the daily baseline. Five is where a median stops
	// being one reading: it survives a single outlier, and the cold first
	// tunnel after a service restart is exactly such an outlier.
	pathRTTMinRecent = 5
)

// Orchestrator runs rollouts. One per hub process.
type Orchestrator struct {
	svc    *Service
	prov   *Provisioner
	health RelayHealth

	// How long each canary run lasts is BenchPolicy's (migration 067), not a
	// field here: it is a knob the owner turns in the console while a
	// rollout is stalled, and a copy on the orchestrator would be a second
	// answer to the same question.
	benchSlack time.Duration // on top of a bench's own length, for a vantage to take it
	benchPoll  time.Duration
	drainFor   time.Duration // how long holders get to move off a box
	sickSince  time.Duration // how far back a probe failure disqualifies a canary
	retryEvery time.Duration // between rounds while a rollout has no verdict
}

// NewOrchestrator builds the rollout runner over the hub's provisioner.
func NewOrchestrator(svc *Service, prov *Provisioner) *Orchestrator {
	return &Orchestrator{
		svc: svc, prov: prov,
		benchSlack: 3 * time.Minute, benchPoll: 5 * time.Second,
		drainFor: 5 * time.Minute, sickSince: 24 * time.Hour, retryEvery: time.Hour,
	}
}

// SetHealth wires the probe history. Unset, no box is held to be sick.
func (o *Orchestrator) SetHealth(h RelayHealth) { o.health = h }

// Rollout is one build's way through the fleet, as the console shows it.
type Rollout struct {
	ID         uuid.UUID  `json:"id"`
	Arch       string     `json:"arch"`
	EdgeSHA    string     `json:"edgeSha"`
	Stage      string     `json:"stage"`
	CanaryBox  string     `json:"canaryBox,omitempty"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

const (
	selectRollout = `
		SELECT id, arch, edge_sha, stage, coalesce(canary_box, ''), coalesce(error, ''), created_at, finished_at
		FROM entry_rollouts`
	rolloutByBuild  = selectRollout + ` WHERE arch = $1 AND edge_sha = $2`
	rolloutsMoving  = selectRollout + ` WHERE arch = $1 AND stage IN ('canary', 'rolling') ORDER BY created_at`
	rolloutByID     = selectRollout + ` WHERE id = $1`
	rolloutsLatest  = selectRollout + ` ORDER BY created_at DESC LIMIT $1`
	insertRollout   = `INSERT INTO entry_rollouts (arch, edge_sha) VALUES ($1, $2) ON CONFLICT DO NOTHING`
	setRolloutBox   = `UPDATE entry_rollouts SET canary_box = $2 WHERE id = $1`
	reopenRollout   = `UPDATE entry_rollouts SET stage = 'rolling', finished_at = NULL WHERE id = $1 AND stage = 'done'`
	setRolloutStage = `
		UPDATE entry_rollouts SET stage = $2, error = $3,
		       finished_at = CASE WHEN $2 IN ('done', 'failed', 'stopped') THEN now() END
		WHERE id = $1 AND stage IN ('canary', 'rolling')`
	// A box is in the fleet while it has a relay that is not dead; its holders
	// are the live grants of its relays. A suspect relay is not in it: a
	// machine waiting for its second chance is not rolled (its admission
	// would read the ban and stop the rollout), and one that comes back
	// takes the next.
	fleetBoxes = `
		SELECT b.name, coalesce(b.arch, ''), coalesce(b.edge_sha, ''), b.created_at,
		       array_agg(r.name ORDER BY r.name),
		       array_agg(r.created_at ORDER BY r.name),
		       bool_or(r.status = 'alive'),
		       bool_or(r.status IN ('testing', 'draining')),
		       (SELECT count(*) FROM entry_grants g JOIN entry_relays gr ON gr.id = g.relay_id
		        WHERE gr.box = b.name AND g.revoked_at IS NULL),
		       b.proto IN ('hysteria2', 'amneziawg')
		FROM entry_boxes b JOIN entry_relays r ON r.box = b.name AND r.status NOT IN ('dead', 'suspect')
		GROUP BY b.name ORDER BY b.created_at DESC, b.name`
)

func scanRollout(row pgx.Row) (Rollout, error) {
	var r Rollout
	err := row.Scan(&r.ID, &r.Arch, &r.EdgeSHA, &r.Stage, &r.CanaryBox, &r.Error, &r.CreatedAt, &r.FinishedAt)
	return r, err
}

// Rollouts returns the latest rollouts, newest first.
func (s *Service) Rollouts(ctx context.Context, limit int) ([]Rollout, error) {
	rows, err := s.pool.Query(ctx, rolloutsLatest, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rollout{}
	for rows.Next() {
		r, err := scanRollout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) rollout(ctx context.Context, id uuid.UUID) (Rollout, error) {
	return scanRollout(s.pool.QueryRow(ctx, rolloutByID, id))
}

// rolloutOf returns the rollout of a build, creating it at the canary stage.
func (s *Service) rolloutOf(ctx context.Context, arch, sha string) (Rollout, error) {
	if _, err := s.pool.Exec(ctx, insertRollout, arch, sha); err != nil {
		return Rollout{}, err
	}
	return scanRollout(s.pool.QueryRow(ctx, rolloutByBuild, arch, sha))
}

func (s *Service) movingRollouts(ctx context.Context, arch string) ([]Rollout, error) {
	rows, err := s.pool.Query(ctx, rolloutsMoving, arch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rollout
	for rows.Next() {
		r, err := scanRollout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// endRollout moves a rollout to a final stage (or to rolling); a rollout
// already ended — stopped by the owner meanwhile — is left as it is.
func (s *Service) endRollout(ctx context.Context, id uuid.UUID, stage, why string) error {
	var errText any
	if why != "" {
		errText = why
	}
	_, err := s.pool.Exec(ctx, setRolloutStage, id, stage, errText)
	return err
}

// StopRollout is the owner's «стоп»: the rollout ends after the box in hand.
func (s *Service) StopRollout(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, setRolloutStage, id, RolloutStopped, "остановлено владельцем")
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: rollout %s is not moving", ErrRejected, id)
	}
	return nil
}

// fleetBox is a box of the fleet as the rollout sees it.
type fleetBox struct {
	name      string
	arch      string
	sha       string
	createdAt time.Time
	relays    []string
	born      []time.Time // per relay, same order
	alive     bool        // has a relay in the pool
	out       bool        // has a relay out of the pool for a rollout (testing/draining)
	holders   int
	// voice: a Hysteria2 box. Never the canary or its control — the benches
	// drive messenger traffic through a stream relay — but rolled like any
	// box after the build passed on a stream one: the edge binary is one.
	voice bool
}

func (s *Service) fleet(ctx context.Context) ([]fleetBox, error) {
	rows, err := s.pool.Query(ctx, fleetBoxes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []fleetBox
	for rows.Next() {
		var b fleetBox
		if err := rows.Scan(&b.name, &b.arch, &b.sha, &b.createdAt, &b.relays, &b.born, &b.alive, &b.out, &b.holders, &b.voice); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Run brings the fleet onto this image's builds, architecture by
// architecture, and returns when there is nothing left to do — or the context
// ends. Called once at start, after the jobs a previous process left running
// were failed.
//
// A round that ends without a verdict — the path was unsteady, no box was
// healthy enough to be the canary, a box was busy — leaves its rollout moving,
// and Run goes round again every retryEvery until no rollout of this image's
// builds is moving (owner's decision 19.10: no verdict means try later, not
// wait for the next deploy).
func (o *Orchestrator) Run(ctx context.Context) {
	for {
		o.round(ctx)
		if !o.unsettled(ctx) {
			return
		}
		applog.Info(ctx, "entry", "rollout: no verdict yet", applog.F("next_round_in", o.retryEvery.String()))
		select {
		case <-ctx.Done():
			return
		case <-time.After(o.retryEvery):
		}
	}
}

// round is one pass over this image's builds.
func (o *Orchestrator) round(ctx context.Context) {
	for _, build := range o.prov.EdgeBuilds() {
		if err := o.roll(ctx, build); err != nil && ctx.Err() == nil {
			applog.Error(ctx, "entry", "rollout round failed", applog.F("arch", build.Arch), applog.F("sha", build.SHA[:12]), applog.F("err", err.Error()))
		}
	}
}

// unsettled reports whether a rollout of a build this image carries is still
// moving after a round.
func (o *Orchestrator) unsettled(ctx context.Context) bool {
	for _, build := range o.prov.EdgeBuilds() {
		moving, err := o.svc.movingRollouts(ctx, build.Arch)
		if err != nil {
			applog.Error(ctx, "entry", "rollout state unreadable", applog.F("err", err.Error()))
			return false
		}
		for _, m := range moving {
			if m.EdgeSHA == build.SHA {
				return true
			}
		}
	}
	return false
}

func (o *Orchestrator) roll(ctx context.Context, build EdgeBuild) error {
	if err := o.survey(ctx); err != nil {
		return err
	}
	if err := o.recover(ctx, build); err != nil {
		return err
	}
	ro, err := o.current(ctx, build)
	if err != nil || ro == nil {
		return err
	}
	if ro.Stage == RolloutCanary {
		if err := o.canary(ctx, build, ro); err != nil {
			return err
		}
		if *ro, err = o.svc.rollout(ctx, ro.ID); err != nil {
			return err
		}
	}
	if ro.Stage == RolloutRolling {
		return o.rolling(ctx, build, ro)
	}
	return nil
}

// current returns the rollout to drive for this build, or nil when the fleet
// already runs it (or it was tried and failed, or the owner stopped it).
func (o *Orchestrator) current(ctx context.Context, build EdgeBuild) (*Rollout, error) {
	behind, err := o.behind(ctx, build)
	if err != nil {
		return nil, err
	}
	if len(behind) == 0 {
		if moving, err := o.svc.movingRollouts(ctx, build.Arch); err == nil {
			for _, m := range moving {
				_ = o.svc.endRollout(ctx, m.ID, RolloutDone, "")
			}
		}
		return nil, nil
	}
	ro, err := o.svc.rolloutOf(ctx, build.Arch, build.SHA)
	if err != nil {
		return nil, err
	}
	switch ro.Stage {
	case RolloutFailed, RolloutStopped:
		applog.Info(ctx, "entry", "rollout ended earlier, boxes stay on their build",
			applog.F("arch", build.Arch), applog.F("sha", build.SHA[:12]), applog.F("stage", ro.Stage),
			applog.F("error", ro.Error), applog.F("boxes", len(behind)))
		return nil, nil
	case RolloutDone:
		// A box came back behind after the rollout ended (one of its relays
		// was restored from elsewhere); it is still this build's to move.
		if _, err := o.svc.pool.Exec(ctx, reopenRollout, ro.ID); err != nil {
			return nil, err
		}
		ro.Stage = RolloutRolling
	}
	return &ro, nil
}

// behind is the boxes of this architecture whose build is not this one.
func (o *Orchestrator) behind(ctx context.Context, build EdgeBuild) ([]fleetBox, error) {
	all, err := o.svc.fleet(ctx)
	if err != nil {
		return nil, err
	}
	var out []fleetBox
	for _, b := range all {
		if b.arch == build.Arch && b.sha != build.SHA {
			out = append(out, b)
		}
	}
	return out, nil
}

// survey reads the build off every box whose version the hub never saw (a box
// provisioned before versions were recorded): without it the box's arch is
// unknown and no rollout would ever reach it.
func (o *Orchestrator) survey(ctx context.Context) error {
	all, err := o.svc.fleet(ctx)
	if err != nil {
		return err
	}
	for _, b := range all {
		if b.sha != "" && b.arch != "" {
			continue
		}
		j := &jobRun{p: o.prov, ctx: ctx, box: b.name}
		r, err := j.loginByKey()
		if err != nil {
			applog.Warn(ctx, "entry", "rollout survey: box unreachable", applog.F("box", b.name), applog.F("err", err.Error()))
			continue
		}
		arch, err := j.arch(r)
		if err == nil {
			if sha := j.edgeOnBox(r, arch); sha != "" {
				applog.Info(ctx, "entry", "rollout survey: box build", applog.F("box", b.name), applog.F("arch", arch), applog.F("sha", sha[:12]))
			}
		}
		r.Close()
	}
	return nil
}

// recover settles what a previous process left mid-way for this architecture.
func (o *Orchestrator) recover(ctx context.Context, build EdgeBuild) error {
	moving, err := o.svc.movingRollouts(ctx, build.Arch)
	if err != nil {
		return err
	}
	for _, ro := range moving {
		if ro.Stage == RolloutCanary && ro.CanaryBox != "" {
			// A canary cut off by a restart says nothing about the build: put
			// the old one back and let the canary run again from the start.
			applog.Warn(ctx, "entry", "rollout: canary cut off, rolling it back", applog.F("sha", ro.EdgeSHA[:12]), applog.F("box", ro.CanaryBox))
			if err := o.prov.execJob(ctx, JobCanary, ro.CanaryBox, ro.ID, func(j *jobRun) error {
				return j.undoCanary(ro.EdgeSHA)
			}); err != nil {
				return fmt.Errorf("откат прерванной канарейки %s: %w", ro.CanaryBox, err)
			}
			if _, err := o.svc.pool.Exec(ctx, setRolloutBox, ro.ID, nil); err != nil {
				return err
			}
		}
		if ro.EdgeSHA != build.SHA {
			// This image no longer carries that build: nothing to finish it with.
			if err := o.svc.endRollout(ctx, ro.ID, RolloutStopped, "вышла новая сборка хаба "+build.SHA[:12]); err != nil {
				return err
			}
		}
	}
	// A box left draining by an update that was cut off goes back to the
	// pool; the rolling pass updates it again if it is still behind.
	all, err := o.svc.fleet(ctx)
	if err != nil {
		return err
	}
	for _, b := range all {
		if b.arch == build.Arch && b.out {
			if back, err := o.svc.Restore(ctx, b.name); err != nil {
				return err
			} else if len(back) > 0 {
				applog.Info(ctx, "entry", "rollout: box back in the pool", applog.F("box", b.name), applog.F("relays", back))
			}
		}
	}
	return nil
}

// canary tries the build on one box and moves the rollout on — to rolling,
// or to failed.
func (o *Orchestrator) canary(ctx context.Context, build EdgeBuild, ro *Rollout) error {
	cands, err := o.canaryCandidates(ctx, build)
	if err != nil {
		return err
	}
	if len(cands) == 0 {
		applog.Info(ctx, "entry", "rollout: no healthy box to try the build on yet", applog.F("arch", build.Arch), applog.F("sha", build.SHA[:12]))
		return nil
	}
	var verdict error
	for i, box := range cands {
		// The control is another healthy box still on the old build, benched
		// beside the canary; without one the canary is judged on its own.
		control := ""
		for k, c := range cands {
			if k != i {
				control = c.name
				break
			}
		}
		if _, err := o.svc.pool.Exec(ctx, setRolloutBox, ro.ID, box.name); err != nil {
			return err
		}
		verdict = o.prov.execJob(ctx, JobCanary, box.name, ro.ID, func(j *jobRun) error {
			return j.canary(o, build, control)
		})
		if !errors.Is(verdict, ErrBoxBusy) {
			break
		}
	}
	switch {
	case errors.Is(verdict, ErrBoxBusy):
		return nil // every candidate had a job running; the next start tries again
	case errors.Is(verdict, ErrBenchBaseline), errors.Is(verdict, ErrBenchUnsteady),
		errors.Is(verdict, errCanaryInconclusive):
		// The old build could not be measured, the path moved under the runs,
		// or the box broke for reasons of its own: no verdict on this build.
		// It stays a canary for next time.
		_, _ = o.svc.pool.Exec(ctx, setRolloutBox, ro.ID, nil)
		return verdict
	case verdict != nil:
		return o.svc.endRollout(ctx, ro.ID, RolloutFailed, verdict.Error())
	}
	return o.svc.endRollout(ctx, ro.ID, RolloutRolling, "")
}

// canaryCandidates are the behind boxes in the pool, newest first, without a
// probe failure lately.
func (o *Orchestrator) canaryCandidates(ctx context.Context, build EdgeBuild) ([]fleetBox, error) {
	behind, err := o.behind(ctx, build)
	if err != nil {
		return nil, err
	}
	var sick map[string]time.Time
	if o.health != nil {
		slots, err := o.activeSlots(ctx)
		if err == nil {
			sick, err = o.health.FailingRelays(ctx, time.Now().Add(-o.sickSince), slots)
		}
		if err != nil {
			applog.Warn(ctx, "entry", "rollout: probe history unavailable, no box held sick", applog.F("err", err.Error()))
			sick = nil
		}
	}
	var out []fleetBox
next:
	for _, b := range behind { // fleet order: newest first
		if !b.alive || b.voice {
			continue
		}
		// A failure counts only if it is this relay's: one from before it was
		// born belongs to an earlier relay under the same name (a pool emptied
		// and refilled, a box reinstalled) — the stand's ams1-1 sat out its
		// first rollout over a predecessor's verdicts.
		for i, r := range b.relays {
			if at, ok := sick[r]; ok && at.After(b.born[i]) {
				continue next
			}
		}
		out = append(out, b)
	}
	return out, nil
}

// rolling moves the rest of the fleet, one box at a time, emptiest first.
func (o *Orchestrator) rolling(ctx context.Context, build EdgeBuild, ro *Rollout) error {
	for {
		cur, err := o.svc.rollout(ctx, ro.ID)
		if err != nil {
			return err
		}
		if cur.Stage != RolloutRolling {
			return nil // stopped by the owner
		}
		behind, err := o.behind(ctx, build)
		if err != nil {
			return err
		}
		if len(behind) == 0 {
			return o.svc.endRollout(ctx, ro.ID, RolloutDone, "")
		}
		sort.SliceStable(behind, func(i, j int) bool { return behind[i].holders < behind[j].holders })
		box := behind[0]
		err = o.prov.execJob(ctx, JobUpdate, box.name, ro.ID, func(j *jobRun) error {
			return j.update(o, build)
		})
		switch {
		case errors.Is(err, ErrBoxBusy):
			return nil // a job of the owner's is on it; the next start goes on
		case err != nil:
			return o.svc.endRollout(ctx, ro.ID, RolloutFailed, "машина "+box.name+": "+err.Error())
		}
	}
}

// errCanaryInconclusive: the canary could not be judged for reasons that are
// not the build's (the box unreachable, the drain never ran).
var errCanaryInconclusive = errors.New("канарейку не удалось оценить, вердикта нет")

// execJob runs body as a job of kind on box, in this goroutine, and returns
// its verdict; ErrBoxBusy when the box already has a job running.
func (p *Provisioner) execJob(ctx context.Context, kind, box string, rollout uuid.UUID, body func(*jobRun) error) error {
	id, err := p.svc.startJobOf(ctx, kind, box, "", rollout)
	if err != nil {
		return err
	}
	jctx, cancel := context.WithTimeout(ctx, p.jobTimeout)
	defer cancel()
	j := &jobRun{p: p, ctx: jctx, id: id, box: box}
	err = body(j)
	j.finish(err, "готово")
	return err
}

// loginByKey opens the box with the hub's key: a rollout never has a password.
// A box with no host key on record is refused unless the job may trust on
// first use (jobRun.firstUse) — see login.
func (j *jobRun) loginByKey() (Remote, error) {
	acc, err := j.p.svc.boxAccess(j.ctx, j.box)
	if err != nil {
		return nil, err
	}
	j.acc = acc
	hubKey, hubPub, err := j.p.svc.HubSSHKey(j.ctx)
	if err != nil {
		return nil, fmt.Errorf("ssh-ключ хаба: %w", err)
	}
	return j.login(acc, hubKey, hubPub, "")
}

// boxOpened is a box logged into for a rollout's work.
type boxOpened struct {
	r      Remote
	relays []provRelay
	arch   string
}

func (j *jobRun) open(build EdgeBuild) (boxOpened, error) {
	j.step("login")
	r, err := j.loginByKey()
	if err != nil {
		return boxOpened{}, err
	}
	relays, err := j.liveRelays()
	if err != nil {
		r.Close()
		return boxOpened{}, err
	}
	arch, err := j.arch(r)
	if err != nil {
		r.Close()
		return boxOpened{}, err
	}
	if arch != build.Arch {
		r.Close()
		return boxOpened{}, fmt.Errorf("машина %s, а сборка под %s", arch, build.Arch)
	}
	return boxOpened{r: r, relays: relays, arch: arch}, nil
}

// drain takes the box's relays out of the pool and waits for their holders.
func (j *jobRun) drain(o *Orchestrator, b boxOpened, status string) error {
	j.step("drain")
	relays, holders, err := j.p.svc.Drain(j.ctx, j.box, status)
	if err != nil {
		return err
	}
	j.log("из пула ушли релеев: %d, держателей разбужено: %d", relays, holders)
	_, err = j.waitEmpty(b.r, b.relays, o.drainFor)
	return err
}

// back returns the box's relays to the pool.
func (j *jobRun) back() {
	names, err := j.p.svc.Restore(j.ctx, j.box)
	if err != nil {
		j.log("возврат в пул не удался: %v", err)
		return
	}
	if len(names) > 0 {
		j.log("снова в пуле: %s", strings.Join(names, ", "))
	}
}

// swap puts the build under every relay of the box.
func (j *jobRun) swap(b boxOpened, build EdgeBuild) error {
	for _, rel := range b.relays {
		j.step("deploy " + rel.name)
		if err := j.deploy(b.r, build, rel); err != nil {
			return fmt.Errorf("разворот %s: %w", rel.name, err)
		}
	}
	return nil
}

// bench runs one bench of the box — beside the control box when there is
// one — and waits for it.
func (j *jobRun) bench(o *Orchestrator, phase, control string, runFor time.Duration) (BenchResult, error) {
	j.step("bench-" + phase)
	id, err := j.p.svc.QueueBench(j.ctx, j.id, j.box, control, phase, runFor)
	if err != nil {
		return BenchResult{}, err
	}
	if control != "" {
		j.log("бенч %s поставлен точке обзора вместе с контролем %s (%s)", phase, control, runFor)
	} else {
		j.log("бенч %s поставлен точке обзора (%s)", phase, runFor)
	}
	res, err := j.p.svc.WaitBench(j.ctx, id, runFor, o.benchSlack, o.benchPoll)
	if err != nil {
		return BenchResult{}, err
	}
	j.log("бенч %s: %s", phase, benchBrief(res))
	if res.Control != nil {
		j.log("контроль %s: %s", control, benchBrief(*res.Control))
	}
	return res, nil
}

// pathSteady says why the probes of relay in [from, to) are off the path's
// daily level, or "" when they are not (or there is no history to tell).
func (o *Orchestrator) pathSteady(ctx context.Context, relay string, from, to time.Time) string {
	if o.health == nil {
		return ""
	}
	slots, err := o.activeSlots(ctx)
	if err != nil {
		return ""
	}
	base, n, err := o.health.RelayRTT(ctx, relay, from.Add(-pathRTTBaseline), from, slots)
	if err != nil || n < pathRTTMinSample || base <= 0 {
		return ""
	}
	now, m, err := o.health.RelayRTT(ctx, relay, from, to, slots)
	// A floor on this side too. It used to be `m == 0` alone, so a window
	// holding ONE probe produced a verdict — and the one probe right after a
	// swap is the service's cold first tunnel, seconds of handshake against a
	// daily median in the hundreds of milliseconds. That is a measurement of
	// the restart, not of the path, and it threw away a build that was fine on
	// the stand on 24.10.
	if err != nil || m < pathRTTMinRecent {
		return ""
	}
	if now > base*(1+pathRTTDrift) || now < base*(1-pathRTTDrift) {
		return fmt.Sprintf("rtt проб %s %.0f мс против обычных %.0f", relay, now, base)
	}
	return ""
}

// canary is the canary job (see the file comment, step 2–3). With a control
// box the two are benched together and the build is judged against it;
// alone, the path must stay steady around the runs for them to count.
func (j *jobRun) canary(o *Orchestrator, build EdgeBuild, control string) (err error) {
	b, err := j.open(build)
	if err != nil {
		return fmt.Errorf("%w: %v", errCanaryInconclusive, err)
	}
	defer b.r.Close()
	if control != "" {
		j.log("канарейка сборки %s %s…, контроль %s", build.Arch, build.SHA[:12], control)
	} else {
		j.log("канарейка сборки %s %s…, контрольной машины нет", build.Arch, build.SHA[:12])
	}
	lone := control == ""
	relay := b.relays[0].name
	// Read once for the whole canary: a knob turned halfway through would
	// make the pairs incomparable, which is the one thing averaging them is
	// supposed to fix.
	pol, err := j.p.svc.BenchPolicy(j.ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", errCanaryInconclusive, err)
	}
	j.log("канарейка мерит %d пар(ы) по %s, порог приёма %d%%, роста rtt %d%% — всего %s бенча",
		pol.Pairs, pol.RunDuration, pol.MinThroughputPct(), pol.MaxRTTGrowthPct(), pol.Benching())
	if lone {
		if why := o.pathSteady(j.ctx, relay, time.Now().Add(-pathRTTRecent), time.Now()); why != "" {
			return fmt.Errorf("%w: путь неспокоен до начала, %s", ErrBenchUnsteady, why)
		}
	}
	if err := j.drain(o, b, StatusTesting); err != nil {
		j.back()
		return fmt.Errorf("%w: %v", errCanaryInconclusive, err)
	}
	before, err := j.bench(o, BenchBefore, control, pol.RunDuration)
	if err == nil {
		err = j.baseline(before, lone, pol)
	}
	if err != nil {
		j.back()
		return err
	}
	prior := j.edgeOnBox(b.r, b.arch)
	defer j.settle(b, prior, &err)
	after, err := j.afterRun(o, b, build, control, relay, lone, pol)
	if err != nil {
		return err
	}
	befores, afters := []BenchResult{before}, []BenchResult{after}

	// The rest of the pairs. Every verdict is the average of `pairs` of them
	// (BenchPolicy, migration 067): one pair cannot tell a slower build from a
	// slower minute, and on this path it demonstrably did not — the same
	// unchanged binary measured 15.4 … 63.4 Mbit/s over a week. The pairs run
	// unconditionally, where the ABAB they replace ran a second pair only
	// after a throughput failure: a verdict whose confidence depends on which
	// way the first pair happened to fall is not a verdict.
	for i := 2; i <= pol.Pairs; i++ {
		j.log("пара %d из %d", i, pol.Pairs)
		before, after, err = j.nextPair(o, b, build, prior, control, relay, lone, pol)
		if err != nil {
			return err
		}
		befores, afters = append(befores, before), append(afters, after)
	}
	pooledBefore, pooledAfter := PoolRuns(befores), PoolRuns(afters)
	if pol.Pairs > 1 {
		j.log("по %d парам: до %s; после %s", pol.Pairs, benchBrief(pooledBefore), benchBrief(pooledAfter))
	}

	if !lone {
		ok, reasons, err := CompareWithControl(pooledBefore, pooledAfter, pol)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("сборка хуже прежней: %s", strings.Join(reasons, "; "))
		}
		j.log("сборка не хуже контроля, раскатка идёт дальше")
		return nil
	}
	ok, reasons, err := CompareBench(pooledBefore, pooledAfter, pol)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("сборка хуже прежней: %s", strings.Join(reasons, "; "))
	}
	j.log("сборка не хуже прежней, раскатка идёт дальше")
	return nil
}

// afterRun is the second half of a pair: the new build on, admitted, and
// benched. Its own function because every pair does it and the first one is
// not special.
func (j *jobRun) afterRun(o *Orchestrator, b boxOpened, build EdgeBuild, control, relay string,
	lone bool, pol BenchPolicy) (BenchResult, error) {
	if err := j.swap(b, build); err != nil {
		return BenchResult{}, err
	}
	swapped := time.Now()
	j.step("probe")
	if err := j.probe(b.relays); err != nil {
		return BenchResult{}, err
	}
	if lone {
		// The admission probe just taken is the path's level after the swap.
		if why := o.pathSteady(j.ctx, relay, swapped, time.Now().Add(time.Second)); why != "" {
			return BenchResult{}, fmt.Errorf("%w: после свапа %s", ErrBenchUnsteady, why)
		}
	}
	after, err := j.bench(o, BenchAfter, control, pol.RunDuration)
	if err != nil {
		return BenchResult{}, err
	}
	if after.Error == "" && after.Drops == 0 {
		if why := Steady(after); why != "" {
			return BenchResult{}, fmt.Errorf("%w: прогон после, %s", ErrBenchUnsteady, why)
		}
	}
	return after, nil
}

// baseline checks the before run can serve as one: it ran, nothing broke —
// and, for a lone canary, the path was steady while it ran.
func (j *jobRun) baseline(before BenchResult, lone bool, pol BenchPolicy) error {
	if !lone {
		_, _, err := CompareWithControl(before, before, pol)
		return err
	}
	if _, _, err := CompareBench(before, before, pol); err != nil {
		return err
	}
	if why := Steady(before); why != "" {
		return fmt.Errorf("%w: прогон до, %s", ErrBenchUnsteady, why)
	}
	return nil
}

// nextPair runs one more before/after pair: the old build back, a before run,
// the new build again, an after run. Returned unpooled — the caller averages
// every pair at once, and folding them two at a time would weight the last
// pair as heavily as all the earlier ones together.
func (j *jobRun) nextPair(o *Orchestrator, b boxOpened, build EdgeBuild, prior, control, relay string,
	lone bool, pol BenchPolicy) (BenchResult, BenchResult, error) {
	if now := j.edgeOnBox(b.r, b.arch); now != prior {
		j.step("rollback")
		if err := j.rollback(b.r, b.arch, b.relays); err != nil {
			return BenchResult{}, BenchResult{}, err
		}
	}
	before, err := j.bench(o, BenchBefore, control, pol.RunDuration)
	if err == nil {
		err = j.baseline(before, lone, pol)
	}
	if err != nil {
		return BenchResult{}, BenchResult{}, err
	}
	after, err := j.afterRun(o, b, build, control, relay, lone, pol)
	if err != nil {
		return BenchResult{}, BenchResult{}, err
	}
	return before, after, nil
}

// settle ends a job that reached the swap: on failure the build the box ran
// before goes back — but only if the swap changed it (a binary already in
// place has nothing to roll back to, and no .prev either) — and the relays
// return to the pool either way.
func (j *jobRun) settle(b boxOpened, prior string, err *error) {
	if *err != nil {
		switch now := j.edgeOnBox(b.r, b.arch); {
		case now == prior:
			j.log("бинарь не менялся (%s…), откатывать нечего", shortSHA(prior))
		default:
			j.step("rollback")
			if rerr := j.rollback(b.r, b.arch, b.relays); rerr != nil {
				j.log("откат не удался: %v", rerr)
				*err = fmt.Errorf("%w; откат не удался: %v", *err, rerr)
			}
		}
	}
	j.back()
}

func shortSHA(sha string) string {
	if len(sha) < 12 {
		return sha
	}
	return sha[:12]
}

// update is the job for every box after the canary (step 4).
func (j *jobRun) update(o *Orchestrator, build EdgeBuild) (err error) {
	b, err := j.open(build)
	if err != nil {
		return err
	}
	defer b.r.Close()
	if err := j.drain(o, b, StatusDraining); err != nil {
		j.back()
		return err
	}
	prior := j.edgeOnBox(b.r, b.arch)
	defer j.settle(b, prior, &err)
	if err := j.swap(b, build); err != nil {
		return err
	}
	j.step("probe")
	return j.probe(b.relays)
}

// undoCanary settles a canary a restart cut off: the old build back if the
// new one went on, the relays back in the pool.
func (j *jobRun) undoCanary(sha string) error {
	j.step("login")
	r, err := j.loginByKey()
	if err != nil {
		return err
	}
	defer r.Close()
	relays, err := j.liveRelays()
	if err != nil {
		return err
	}
	arch, err := j.arch(r)
	if err != nil {
		return err
	}
	if j.edgeOnBox(r, arch) == sha {
		j.step("rollback")
		if err := j.rollback(r, arch, relays); err != nil {
			return err
		}
	}
	j.back()
	return nil
}

// RunOnBox runs one command on a box in service with the hub's key and
// returns its output — the operator's way onto a machine nobody but the hub
// holds a key to (`cmd/server entry-box ssh`, the chaos runs of П9.7).
//
// Only against a host key the hub already recorded: a first login is a
// provisioning job's, which pins the key it saw, and a shell must not be the
// thing that trusts a machine for the first time. Not a job either — it
// takes no per-box lock and writes no job log — so it leaves its trail in
// the hub's own log instead.
func (p *Provisioner) RunOnBox(ctx context.Context, box, cmd string, stdin io.Reader) (string, error) {
	acc, err := p.svc.boxAccess(ctx, box)
	if err != nil {
		return "", err
	}
	if acc.retired {
		return "", fmt.Errorf("%w: %s", ErrBoxRetired, box)
	}
	if acc.hostKey == "" {
		return "", fmt.Errorf("%w: ключ хоста %s ещё не записан — сначала разворот", ErrRejected, box)
	}
	hubKey, _, err := p.svc.HubSSHKey(ctx)
	if err != nil {
		return "", fmt.Errorf("ssh-ключ хаба: %w", err)
	}
	r, err := p.dialBox(ctx, box, acc, ssh.PublicKeys(hubKey))
	if err != nil {
		return "", err
	}
	defer r.Close()
	applog.Info(ctx, "entry", "operator command on box", applog.F("box", box), applog.F("cmd", cmd))
	return r.Run(ctx, cmd, stdin)
}
