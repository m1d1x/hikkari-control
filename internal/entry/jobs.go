package entry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
)

// Jobs and probe tasks (migration 043): the record of every provisioning run,
// and the admission probes the hub asks of the vantages.

const (
	insertJob = `
		INSERT INTO entry_jobs (box, kind, admin_id, rollout_id) VALUES ($1, $2, $3, $4) RETURNING id`
	setJobStep = `UPDATE entry_jobs SET step = $2 WHERE id = $1`
	finishJob  = `
		UPDATE entry_jobs SET status = $2, error = $3, finished_at = now()
		WHERE id = $1 AND status = 'running'`
	// A hub that restarts loses the goroutines of its running jobs; their rows
	// must not say "running" forever (and block the box).
	abandonJobs = `
		UPDATE entry_jobs SET status = 'failed', error = 'хаб перезапустился посреди задачи', finished_at = now()
		WHERE status = 'running'`
	appendJobLog = `
		INSERT INTO entry_job_log (job_id, seq, line)
		SELECT $1, coalesce(max(seq), 0) + 1, $2 FROM entry_job_log WHERE job_id = $1`
	selectJob = `
		SELECT id, box, kind, status, step, error, created_at, finished_at FROM entry_jobs WHERE id = $1`
	listJobs = `
		SELECT id, box, kind, status, step, error, created_at, finished_at FROM entry_jobs
		ORDER BY created_at DESC LIMIT $1`
	selectJobLog = `
		SELECT seq, at, line FROM entry_job_log WHERE job_id = $1 AND seq > $2 ORDER BY seq`

	insertProbeTask = `
		INSERT INTO entry_probe_tasks (job_id, relays) VALUES ($1, $2) RETURNING id`
	// Oldest first of the kind asked for; SKIP LOCKED so two vantages never
	// take the same task.
	//
	// Only a task somebody still waits for: one tied to a job that is no
	// longer running (it gave up at its deadline, or died with the process
	// and was failed by AbandonRunningJobs) stays queued forever, and being
	// the oldest it would be handed out first — the vantage spends its round
	// on relays nobody reads the verdict of, and the live job behind it waits
	// one round longer, every time. A task with no job (a manual bench) is
	// read through the API and is always wanted.
	//
	// And only by an active slot: `by` comes from the vantage's token, and
	// the slot is checked here once more so a slot retired between the two
	// claims nothing.
	claimProbeTask = `
		UPDATE entry_probe_tasks SET status = 'claimed', claimed_by = $1, claimed_at = now()
		WHERE id = (SELECT t.id FROM entry_probe_tasks t
		            WHERE t.status = 'queued' AND t.kind = $2
		              AND (t.job_id IS NULL OR EXISTS (
		                  SELECT 1 FROM entry_jobs j WHERE j.id = t.job_id AND j.status = 'running'))
		            ORDER BY t.created_at LIMIT 1 FOR UPDATE OF t SKIP LOCKED)
		  AND EXISTS (SELECT 1 FROM entry_vantages v WHERE v.slot = $1 AND v.status = 'active')
		RETURNING id, kind, relays, coalesce(params, '{}'::jsonb)`
	// The task as its claimer left it, locked for the finish. Only the slot
	// that claimed it may answer it (audit 42-autopilot В2).
	claimedProbeTask = `
		SELECT kind, relays, coalesce(params, '{}'::jsonb) FROM entry_probe_tasks
		WHERE id = $1 AND status = 'claimed' AND claimed_by = $2
		FOR UPDATE`
	// A bench must come back with its result; an admission probe and the
	// reprobes have none.
	finishProbeTask = `
		UPDATE entry_probe_tasks SET status = 'done', verdicts = $2, result = $3, done_at = now()
		WHERE id = $1 AND status = 'claimed' AND (kind = 'bench') = ($3::jsonb IS NOT NULL)`
	selectProbeTask = `SELECT status, verdicts FROM entry_probe_tasks WHERE id = $1`
	// The credentials a prober dials with, for the named relays, in the order
	// named: a bench's relays are the canary's, then the control's, and a
	// prober reads the first as the canary.
	probeRelays = `
		SELECT r.name, r.box, r.identity_ct, b.address_ct
		FROM entry_relays r JOIN entry_boxes b ON b.name = r.box
		WHERE r.name = ANY($1) ORDER BY array_position($1, r.name)`
	// Every relay that physically carries traffic, not just the ones the pool
	// hands out: a relay drained for a rollout (`testing`, `draining`) is the
	// one the canary is measuring, and it is exactly then that its path must
	// stay under observation.
	watchedRelayNames = `SELECT name FROM entry_relays
		WHERE status IN ('alive', 'testing', 'draining') ORDER BY name`
	deadRelayNames = `SELECT name FROM entry_relays
		WHERE name = ANY($1) AND status = 'dead'`
)

// Job is one provisioning run as the console shows it.
type Job struct {
	ID         uuid.UUID  `json:"id"`
	Box        string     `json:"box"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"` // running | done | failed
	Step       string     `json:"step"`
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// JobLine is one line of a job's log.
type JobLine struct {
	Seq  int       `json:"seq"`
	At   time.Time `json:"at"`
	Line string    `json:"line"`
}

func scanJob(row pgx.Row) (Job, error) {
	var j Job
	var errText *string
	if err := row.Scan(&j.ID, &j.Box, &j.Kind, &j.Status, &j.Step, &errText, &j.CreatedAt, &j.FinishedAt); err != nil {
		return Job{}, err
	}
	if errText != nil {
		j.Error = *errText
	}
	return j, nil
}

// Job returns one job, or ErrNoSuchJob.
func (s *Service) Job(ctx context.Context, id uuid.UUID) (Job, error) {
	j, err := scanJob(s.pool.QueryRow(ctx, selectJob, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNoSuchJob
	}
	return j, err
}

// Jobs returns the latest jobs, newest first.
func (s *Service) Jobs(ctx context.Context, limit int) ([]Job, error) {
	rows, err := s.pool.Query(ctx, listJobs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// JobLog returns the lines of a job after seq `after` (0 = from the start).
func (s *Service) JobLog(ctx context.Context, id uuid.UUID, after int) ([]JobLine, error) {
	rows, err := s.pool.Query(ctx, selectJobLog, id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []JobLine{}
	for rows.Next() {
		var l JobLine
		if err := rows.Scan(&l.Seq, &l.At, &l.Line); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// AbandonRunningJobs fails every job left running by a previous process.
// Called once at start, before any new job can begin.
func (s *Service) AbandonRunningJobs(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, abandonJobs)
	return tag.RowsAffected(), err
}

// ErrNoSuchJob: no job with that id.
var ErrNoSuchJob = errors.New("entry: no such job")

// ErrBoxBusy: the box already has a running job.
var ErrBoxBusy = errors.New("entry: the box has a job running")

// ProbeRelay is a relay as a prober dials it — the client-side credentials,
// exactly as a real client would use them (cmd/entry-prober edge). SECRETS:
// served only behind the prober token.
//
// A voice relay (Proto hysteria2) carries the Hysteria credentials instead of
// the Reality ones, its finalmask already rendered (the prober dials with it
// as the edge serves it), and the loopback echo port its admission probe aims
// at through the tunnel.
type ProbeRelay struct {
	Name      string   `json:"name"`
	Box       string   `json:"box"`
	Address   string   `json:"address"`
	Port      int      `json:"port"`
	Proto     string   `json:"proto"`
	UUID      string   `json:"uuid,omitempty"`
	PublicKey string   `json:"public_key,omitempty"`
	ShortID   string   `json:"short_id,omitempty"`
	Covers    []string `json:"covers,omitempty"`

	Auth       string           `json:"auth,omitempty"`
	ServerName string           `json:"server_name,omitempty"`
	CertSHA256 string           `json:"cert_sha256,omitempty"`
	FinalMask  []map[string]any `json:"finalmask,omitempty"`
	EchoPort   int              `json:"echo_port,omitempty"`

	// An XHTTP relay (Proto vless-xhttp, П4в): UUID above, and the domain,
	// secret path and mode it is dialled with.
	Host string `json:"host,omitempty"`
	Path string `json:"path,omitempty"`
	Mode string `json:"mode,omitempty"`
}

// rowsQuerier is what ProbeRelays reads through: the pool, or the claim's
// transaction.
type rowsQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// ProbeRelays opens the dialling credentials of the named relays.
func (s *Service) ProbeRelays(ctx context.Context, names []string) ([]ProbeRelay, error) {
	return probeRelaysIn(ctx, s.pool, names)
}

func probeRelaysIn(ctx context.Context, q rowsQuerier, names []string) ([]ProbeRelay, error) {
	rows, err := q.Query(ctx, probeRelays, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProbeRelay{}
	for rows.Next() {
		var name, box string
		var idCT, addrCT []byte
		if err := rows.Scan(&name, &box, &idCT, &addrCT); err != nil {
			return nil, err
		}
		id, err := openIdentity(idCT)
		if err != nil {
			return nil, fmt.Errorf("entry: relay %s: %w", name, err)
		}
		addr, err := cryptobox.DecryptString(addrCT)
		if err != nil {
			return nil, fmt.Errorf("entry: box %s: %w", box, err)
		}
		pr := ProbeRelay{Name: name, Box: box, Address: addr, Port: id.Port, Proto: id.proto(), EchoPort: id.echoPort()}
		if h := id.Hysteria; h != nil {
			pr.Auth, pr.ServerName, pr.CertSHA256 = h.Auth, h.ServerName, h.CertSHA256
			pr.FinalMask = xrayFinalmask(h.Masks)
		} else if x := id.Xhttp; x != nil {
			pr.UUID, pr.Host, pr.Path, pr.Mode = id.UUID, x.Domain, x.Path, x.Mode
		} else {
			pr.UUID, pr.PublicKey, pr.ShortID, pr.Covers = id.UUID, id.PublicKey, id.ShortID, id.Covers
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

// WatchedProbeRelays is what the routine probe rounds watch: every relay that
// is physically up, whether or not the pool is handing it out.
//
// It is the whole pool with its credentials, and it has to be: a routine
// round proves a relay by riding its tunnel, as a client would. What keeps
// the list from being a gift is who may ask — an active slot's own token, on
// a machine that is not one of the fleet (vantages.go) — not a shorter list.
// Every other route a vantage reads is cut to the task in hand
// (ClaimProbeTask).
//
// It used to be `alive` alone, and that made the rollout blind itself. The
// canary drains its box (П6.14), every relay leaves `alive`, this answer goes
// empty, the vantage stops probing — and then the canary judges the path by
// `pathSteady`, over probes that stopped because of its own drain. On the
// stand on 24.10 it read the single cold probe taken right after the service
// restart (5360 ms against the usual 335) as «the path is unsteady» and threw
// the build away. A drained relay is still a machine on the wire: it is
// watched, and the detector ignores its verdicts by itself (DiagnoseBox skips
// anything not `alive`), so nothing can evict a relay for failing while it is
// being rebuilt.
func (s *Service) WatchedProbeRelays(ctx context.Context) ([]ProbeRelay, error) {
	rows, err := s.pool.Query(ctx, watchedRelayNames)
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return []ProbeRelay{}, nil
	}
	return s.ProbeRelays(ctx, names)
}

// DeadRelays answers which of names the pool already holds dead. The probe
// ingest drops samples about those (internal/probesink): a round that began
// before a machine was handed back reports on it after its instance is gone,
// and that is not an observation of the censor.
func (s *Service) DeadRelays(ctx context.Context, names []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(names) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, deadRelayNames, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

// Task kinds (entry_probe_tasks.kind).
const (
	TaskAdmission = "admission"
	TaskBench     = "bench"
)

// ProbeTask is a task a vantage claimed: an admission probe of a box's relays,
// a bench through one relay (П6.14 Ф3), a reprobe of burned domains against
// one relay's address (TaskDomain), or of a suspect machine's relays
// (TaskBox, reprobe.go).
type ProbeTask struct {
	ID     uuid.UUID    `json:"id"`
	Kind   string       `json:"kind"`
	Relays []ProbeRelay `json:"relays"`
	// Params: what the hub asked to run ({"durationSec": …} for a bench).
	Params json.RawMessage `json:"params,omitempty"`
}

// ClaimProbeTask hands the oldest queued task of `kind` that a running job
// (or nobody — a manual bench) still waits for to the active slot `by`, or
// nil. `by` is the slot the vantage's token belongs to; a slot that is not
// active claims nothing (ErrRejected).
//
// The claim and the opening of the task's credentials are one transaction: a
// task whose relays cannot be opened goes back to the queue rather than
// staying claimed by a vantage that never received it.
func (s *Service) ClaimProbeTask(ctx context.Context, by, kind string) (*ProbeTask, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if kind == TaskAdmission {
		if err := s.noteAdmissionPoll(ctx, tx, by); err != nil {
			return nil, err
		}
	}

	var t ProbeTask
	var names []string
	var params []byte
	err = tx.QueryRow(ctx, claimProbeTask, by, kind).Scan(&t.ID, &t.Kind, &names, &params)
	if errors.Is(err, pgx.ErrNoRows) {
		var active bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM entry_vantages WHERE slot = $1 AND status = 'active')`, by).Scan(&active); err != nil {
			return nil, err
		}
		if !active {
			return nil, fmt.Errorf("%w: точки наблюдения %q нет среди действующих", ErrRejected, by)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if t.Relays, err = probeRelaysIn(ctx, tx, names); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	t.Params = params
	return &t, nil
}

// ErrNotAnAnswer: the verdicts of a finish do not answer the task — a subject
// it never named, one of its subjects left out, or a word the prober does not
// say for that kind. Wraps ErrRejected.
var ErrNotAnAnswer = errors.New("entry: not an answer to the task")

// The words a prober answers each kind of task with (cmd/entry-prober). The
// hub reads nothing else: a verdict is a word of this vocabulary about a
// subject the task named, or it is not a verdict.
var (
	// A probe of a relay (classify.go, voice.go), and probe_failed: the
	// prober's own engine did not start, which says nothing about the relay.
	relayVerdictWords = map[string]bool{
		VerdictClean: true, VerdictRealityWins: true, VerdictIPBanned: true, VerdictFingerprint: true,
		CauseDomainBlocked: true, VerdictNetworkDown: true, VerdictUnknown: true, VerdictProbeFailed: true,
	}
	// A burned domain tried as SNI (classifyReprobe).
	domainVerdictWords = map[string]bool{
		VerdictClean: true, CauseDomainBlocked: true, VerdictNetworkDown: true, VerdictUnknown: true,
	}
	// A bench through a relay (bench.go).
	benchVerdictWords = map[string]bool{"benched": true, "bench_failed": true}
)

// Verdict words of a relay probe the hub acts on by name.
const (
	VerdictClean       = "clean"
	VerdictRealityWins = "reality_wins"
	VerdictIPBanned    = "ip_banned"
	VerdictFingerprint = "fingerprint"
	VerdictNetworkDown = "network_down"
	VerdictUnknown     = "unknown"
	// VerdictProbeFailed: the prober could not run the probe at all (its xray
	// did not start). The prober's failure, not the relay's.
	VerdictProbeFailed = "probe_failed"
)

// checkAnswer says verdicts answer a task of kind over relays (and params):
// exactly the task's subjects, each in the kind's words.
func checkAnswer(kind string, relays []string, params []byte, verdicts map[string]string) error {
	subjects, words := relays, relayVerdictWords
	switch kind {
	case TaskDomain:
		var p domainTaskParams
		if err := json.Unmarshal(params, &p); err != nil {
			return fmt.Errorf("entry: domain task params: %w", err)
		}
		subjects, words = p.Domains, domainVerdictWords
	case TaskBench:
		words = benchVerdictWords
	}
	if len(verdicts) != len(subjects) {
		return fmt.Errorf("%w: %w: %d verdicts for %d subjects", ErrRejected, ErrNotAnAnswer, len(verdicts), len(subjects))
	}
	for _, n := range subjects {
		v, ok := verdicts[n]
		if !ok {
			return fmt.Errorf("%w: %w: no verdict for a subject of the task", ErrRejected, ErrNotAnAnswer)
		}
		if !words[v] {
			return fmt.Errorf("%w: %w: %q is not a verdict of a %s task", ErrRejected, ErrNotAnAnswer, clip(v, 32), kind)
		}
	}
	return nil
}

// FinishProbeTask records the verdicts of a task the slot `by` claimed, and
// a bench's result (nil for an admission probe — and required for a bench).
// Only the claimer finishes a task, and only with an answer to it
// (checkAnswer). A bench's result also goes to the dataset sink, when one is
// wired; a reprobe's verdicts go to the second chance's ledger in the same
// transaction as the task.
func (s *Service) FinishProbeTask(ctx context.Context, id uuid.UUID, by string, verdicts map[string]string, result *BenchResult) error {
	raw, err := json.Marshal(verdicts)
	if err != nil {
		return err
	}
	var res []byte
	if result != nil {
		if res, err = json.Marshal(result); err != nil {
			return err
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var kind string
	var relays []string
	var params []byte
	err = tx.QueryRow(ctx, claimedProbeTask, id, by).Scan(&kind, &relays, &params)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: task %s is not claimed by %s", ErrRejected, id, by)
	}
	if err != nil {
		return err
	}
	if err := checkAnswer(kind, relays, params, verdicts); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, finishProbeTask, id, raw, res)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: task %s: its result does not fit its kind", ErrRejected, id)
	}
	switch kind {
	case TaskDomain:
		if err := s.applyDomainVerdicts(ctx, tx, params, verdicts); err != nil {
			return err
		}
	case TaskBox:
		if err := s.applyBoxVerdicts(ctx, tx, params, relays, verdicts); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if kind == TaskBench {
		s.recordBench(ctx, id, relays[0], by, *result)
	}
	return nil
}

func (s *Service) createProbeTask(ctx context.Context, job uuid.UUID, relays []string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, insertProbeTask, job, relays).Scan(&id)
	return id, err
}

// probeVerdicts returns the verdicts of a task, or nil while it is not done.
func (s *Service) probeVerdicts(ctx context.Context, id uuid.UUID) (map[string]string, error) {
	var status string
	var raw []byte
	if err := s.pool.QueryRow(ctx, selectProbeTask, id).Scan(&status, &raw); err != nil {
		return nil, err
	}
	if status != "done" {
		return nil, nil
	}
	var v map[string]string
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func (s *Service) startJob(ctx context.Context, box, adminID string) (uuid.UUID, error) {
	return s.startJobOf(ctx, JobProvision, box, adminID, uuid.Nil)
}

// Job kinds (entry_jobs.kind): the console's provisioning, a rollout's work
// on one box (П6.14), and the wipe of a box handed back (П6.10).
const (
	JobProvision = "provision"
	JobCanary    = "canary"
	JobUpdate    = "update"
	JobRetire    = "retire"
	// JobRekey: new Reality credentials on a box that stays where it is
	// (П9.6, playbook rotate_reality).
	JobRekey = "rekey"
	// JobReboot: the machine restarted through the hoster's API and waited
	// for (П9.2, playbook reboot_box).
	JobReboot = "reboot"
	// JobRestart: the relay units the grid found stopped, restarted on a box
	// that is otherwise fine (П9.6б, playbook restart_edge).
	JobRestart = "restart"
	// JobRedest: a relay moved off a dead cover site onto the ones still
	// answering (П9.6б, playbook rotate_dest).
	JobRedest = "redest"
	// JobProber: nekko-entry-prober deployed and started on the box assigned
	// to an observation point slot (К3′ / П9.12).
	JobProber = "prober"
)

func (s *Service) startJobOf(ctx context.Context, kind, box, adminID string, rollout uuid.UUID) (uuid.UUID, error) {
	var admin, rid any
	if adminID != "" {
		admin = adminID
	}
	if rollout != uuid.Nil {
		rid = rollout
	}
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, insertJob, box, kind, admin, rid).Scan(&id)
	if err != nil && isUniqueViolation(err) {
		return uuid.Nil, fmt.Errorf("%w: %s", ErrBoxBusy, box)
	}
	return id, err
}

func (s *Service) jobStep(ctx context.Context, id uuid.UUID, step string) error {
	_, err := s.pool.Exec(ctx, setJobStep, id, step)
	return err
}

func (s *Service) jobLog(ctx context.Context, id uuid.UUID, line string) error {
	_, err := s.pool.Exec(ctx, appendJobLog, id, line)
	return err
}

func (s *Service) finishJob(ctx context.Context, id uuid.UUID, jobErr error) error {
	status, msg := "done", (*string)(nil)
	if jobErr != nil {
		status = "failed"
		m := jobErr.Error()
		msg = &m
	}
	_, err := s.pool.Exec(ctx, finishJob, id, status, msg)
	return err
}

func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}
