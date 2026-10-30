package entry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/m1d1x/hikkari-control/internal/applog"
	"github.com/m1d1x/hikkari-control/internal/entrybench"
)

// Benches (roadmap 42 П6.14 Ф3). A new relay build leaves its canary box only
// if the path through it is no worse than through the old build on the same
// box: the RU vantage drives messenger-shaped traffic through one relay of the
// box to the hub's bench routes (bench_api.go), before and after the swap, and
// the rollout compares the two summaries (CompareBench).
//
// Messenger-shaped, not a speed test: chat exchanges of a few hundred bytes at
// human pace, a voice-like stream of small frames every 20 ms, and photo-sized
// transfers spaced out — the traffic a client of ours makes, so a run neither
// stands out to the censor nor measures something users never do.

// BenchResult is one run as the vantage measured it (internal/entrybench:
// the prober speaks the same type without the hub's dependencies).
type BenchResult = entrybench.Result

// Bench durations the hub asks for. The plan's window is 1–10 minutes.
const (
	DefaultBenchDuration = 5 * time.Minute
	MinBenchDuration     = time.Minute
	MaxBenchDuration     = 10 * time.Minute
)

// The owner's thresholds used to be two constants here. They are [BenchPolicy]
// now (migration 067): the stand proved they are judgements revised while
// looking at a stalled rollout, not facts about the program, and the number of
// runs a verdict rests on turned out to matter more than either of them.

// ErrBenchBaseline: the before run cannot serve as a baseline (it failed or
// broke), so no comparison says anything about the build.
var ErrBenchBaseline = errors.New("прогон до не годится как база, вердикта нет")

// CompareBench judges the after run against the before run on the same box.
// ok=false comes with the reasons, worded for the console; err is
// ErrBenchBaseline when the before run is unusable — then the build is
// neither good nor bad, and the rollout must not guess.
func CompareBench(before, after BenchResult, p BenchPolicy) (ok bool, reasons []string, err error) {
	if before.Error != "" || before.Drops > 0 || before.ChatN == 0 || before.MediaN == 0 {
		return false, nil, fmt.Errorf("%w: %s", ErrBenchBaseline, benchBrief(before))
	}
	if after.Error != "" {
		return false, []string{"прогон после не состоялся: " + after.Error}, nil
	}
	if after.Drops > 0 {
		reasons = append(reasons, fmt.Sprintf("обрывов: %d", after.Drops))
	}
	if after.ChatN == 0 || after.MediaN == 0 {
		reasons = append(reasons, "прогон после пустой")
	}
	if after.DownMbps < before.DownMbps*p.MinThroughputShare {
		reasons = append(reasons, fmt.Sprintf("приём %.1f Мбит/с против %.1f до", after.DownMbps, before.DownMbps))
	}
	if after.UpMbps < before.UpMbps*p.MinThroughputShare {
		reasons = append(reasons, fmt.Sprintf("отдача %.1f Мбит/с против %.1f до", after.UpMbps, before.UpMbps))
	}
	if after.ChatRTTP50Ms > before.ChatRTTP50Ms*p.MaxRTTGrowth {
		reasons = append(reasons, fmt.Sprintf("rtt p50 %.0f мс против %.0f до", after.ChatRTTP50Ms, before.ChatRTTP50Ms))
	}
	return len(reasons) == 0, reasons, nil
}

// ErrBenchUnsteady: the path itself moved during the measurement — late voice
// frames, a control that broke, probe RTT off its usual level — so the runs
// say nothing about the build. Like ErrBenchBaseline, no verdict: the canary
// is tried again later.
var ErrBenchUnsteady = errors.New("путь неспокоен, вердикта нет")

// maxVoiceLateShare is how many voice frames may come back late in a run that
// still counts as a steady path: the clean runs on the stand had none, the
// evening the path drifted had 841 of ~15 000.
const maxVoiceLateShare = 0.005

// Steady reports whether a run saw a quiet path: it ran, and the voice-like
// stream, the most timing-sensitive of the three flows, kept its frames on
// time. "" when steady, else why not.
func Steady(r BenchResult) string {
	switch {
	case r.Error != "":
		return "прогон не состоялся: " + r.Error
	case r.VoiceSent == 0:
		return "голосовой поток не шёл"
	case float64(r.VoiceLate) > float64(r.VoiceSent)*maxVoiceLateShare:
		return fmt.Sprintf("голос: поздних кадров %d из %d", r.VoiceLate, r.VoiceSent)
	}
	return ""
}

// CompareWithControl judges the canary against a control box benched at the
// same time on the old build (before.Control, after.Control): the build is
// worse when the canary lost ground AGAINST THE CONTROL — throughput share
// down more than 20%, RTT ratio up more than 30% — or broke while the
// control did not. Drift on the path the two share cancels out. A control
// that broke, or a baseline that did, is no verdict.
func CompareWithControl(before, after BenchResult, p BenchPolicy) (ok bool, reasons []string, err error) {
	kb, ka := before.Control, after.Control
	usable := func(r *BenchResult) bool {
		return r != nil && r.Error == "" && r.Drops == 0 && r.ChatN > 0 && r.MediaN > 0 &&
			r.DownMbps > 0 && r.UpMbps > 0 && r.ChatRTTP50Ms > 0
	}
	if !usable(&before) || !usable(kb) {
		return false, nil, fmt.Errorf("%w: канарейка %s; контроль %s", ErrBenchBaseline, benchBrief(before), briefOf(kb))
	}
	if after.Error != "" {
		return false, []string{"прогон после не состоялся: " + after.Error}, nil
	}
	if !usable(ka) {
		return false, nil, fmt.Errorf("%w: контроль после %s", ErrBenchUnsteady, briefOf(ka))
	}
	if after.Drops > 0 {
		reasons = append(reasons, fmt.Sprintf("обрывов: %d, у контроля 0", after.Drops))
	}
	if after.ChatN == 0 || after.MediaN == 0 {
		reasons = append(reasons, "прогон после пустой")
	}
	share := func(c, k float64) float64 { return c / k }
	if b, a := share(before.DownMbps, kb.DownMbps), share(after.DownMbps, ka.DownMbps); a < b*p.MinThroughputShare {
		reasons = append(reasons, fmt.Sprintf("приём относительно контроля %.2f против %.2f до", a, b))
	}
	if b, a := share(before.UpMbps, kb.UpMbps), share(after.UpMbps, ka.UpMbps); a < b*p.MinThroughputShare {
		reasons = append(reasons, fmt.Sprintf("отдача относительно контроля %.2f против %.2f до", a, b))
	}
	if b, a := share(before.ChatRTTP50Ms, kb.ChatRTTP50Ms), share(after.ChatRTTP50Ms, ka.ChatRTTP50Ms); a > b*p.MaxRTTGrowth {
		reasons = append(reasons, fmt.Sprintf("rtt относительно контроля %.2f против %.2f до", a, b))
	}
	return len(reasons) == 0, reasons, nil
}

func briefOf(r *BenchResult) string {
	if r == nil {
		return "не прогнан"
	}
	return benchBrief(*r)
}

// PoolRuns folds a whole side of a canary — every `before` run, or every
// `after` run — into the one summary the comparison judges.
//
// # Why an unweighted mean of the runs and not of the bytes
//
// Each run is one sample of the path, and the thing being averaged away is the
// path's drift between runs. Weighting by bytes would give the fastest minute
// the loudest vote, which is the opposite of what more samples are for.
//
// A run that did not happen is returned as itself: a side with a failed run
// has no average worth having, and the comparison says so rather than
// averaging a zero into a verdict.
//
// The control runs ride along, pooled the same way, so a canary judged against
// a control box averages both halves of the ratio over the same minutes.
func PoolRuns(rs []BenchResult) BenchResult {
	if len(rs) == 0 {
		return BenchResult{Error: "прогонов нет"}
	}
	out := rs[0]
	for _, r := range rs[1:] {
		out = PoolBench(out, r)
		if out.Error != "" {
			return out
		}
	}
	// PoolBench works on the runs themselves and knows nothing of the control
	// beside them, so the controls are pooled here — and only when every run
	// has one, since a ratio against a control that was measured half the time
	// is not a ratio.
	controls := make([]BenchResult, 0, len(rs))
	for _, r := range rs {
		if r.Control == nil {
			return out
		}
		controls = append(controls, *r.Control)
	}
	pooled := PoolRuns(controls)
	out.Control = &pooled
	return out
}

// PoolBench folds two runs on one build into one: throughput and RTT
// averaged, counts and breaks summed.
func PoolBench(a, b BenchResult) BenchResult {
	if a.Error != "" {
		return a
	}
	if b.Error != "" {
		return b
	}
	mean := func(x, y float64) float64 { return (x + y) / 2 }
	return BenchResult{
		DurationS: a.DurationS + b.DurationS,
		ChatN:     a.ChatN + b.ChatN, ChatErrors: a.ChatErrors + b.ChatErrors,
		ChatRTTP50Ms: mean(a.ChatRTTP50Ms, b.ChatRTTP50Ms), ChatRTTP95Ms: mean(a.ChatRTTP95Ms, b.ChatRTTP95Ms),
		MediaN: a.MediaN + b.MediaN, MediaErrors: a.MediaErrors + b.MediaErrors,
		DownMbps: mean(a.DownMbps, b.DownMbps), UpMbps: mean(a.UpMbps, b.UpMbps),
		BytesDown: a.BytesDown + b.BytesDown, BytesUp: a.BytesUp + b.BytesUp,
		VoiceSent: a.VoiceSent + b.VoiceSent, VoiceLost: a.VoiceLost + b.VoiceLost, VoiceLate: a.VoiceLate + b.VoiceLate,
		VoiceRTTP50Ms: mean(a.VoiceRTTP50Ms, b.VoiceRTTP50Ms), VoiceJitterMs: mean(a.VoiceJitterMs, b.VoiceJitterMs),
		VoiceDrops: a.VoiceDrops + b.VoiceDrops, Drops: a.Drops + b.Drops,
	}
}

// benchBrief is one line of a run for logs and the console.
func benchBrief(r BenchResult) string {
	if r.Error != "" {
		return "не состоялся: " + r.Error
	}
	return fmt.Sprintf("rtt p50 %.0f мс, приём %.1f Мбит/с, отдача %.1f Мбит/с, голос rtt %.0f мс, обрывов %d",
		r.ChatRTTP50Ms, r.DownMbps, r.UpMbps, r.VoiceRTTP50Ms, r.Drops)
}

// BenchRecord is a finished run as the dataset keeps it: the result, and what
// it was a run of.
type BenchRecord struct {
	At      time.Time
	TaskID  uuid.UUID
	Region  string
	Relay   string
	Box     string
	Arch    string
	EdgeSHA string
	Phase   string
	Result  BenchResult
}

// BenchSink takes finished runs into the dataset (ClickHouse entry_benches).
// Best effort: the rollout reads its decision from Postgres.
type BenchSink interface {
	InsertEntryBench(ctx context.Context, r BenchRecord) error
}

// SetBenchSink wires the dataset. Called once at startup; unset, runs are
// kept in Postgres only.
func (s *Service) SetBenchSink(sink BenchSink) { s.benches = sink }

// Bench phases: which side of a swap a run measured.
const (
	BenchBefore = "before"
	BenchAfter  = "after"
	BenchManual = "manual"
)

type benchParams struct {
	DurationSec int `json:"durationSec"`
	// Phase and the build the box ran when the bench was queued, so the
	// dataset row says what it measured even if the box moved on since.
	Phase   string `json:"phase"`
	Arch    string `json:"arch,omitempty"`
	EdgeSHA string `json:"edgeSha,omitempty"`
	// The control box benched beside it, and its build; empty for a lone box.
	Control        string `json:"control,omitempty"`
	ControlRelay   string `json:"controlRelay,omitempty"`
	ControlEdgeSHA string `json:"controlEdgeSha,omitempty"`
}

const (
	// The relay a bench of a box goes through: one that is deployed and
	// admitted — alive, or out of the pool for the rollout itself.
	benchRelayOfBox = `
		SELECT r.name, coalesce(b.arch, ''), coalesce(b.edge_sha, '')
		FROM entry_relays r JOIN entry_boxes b ON b.name = r.box
		WHERE r.box = $1 AND r.status IN ('alive', 'testing', 'draining')
		ORDER BY r.id LIMIT 1`
	insertBenchTask = `
		INSERT INTO entry_probe_tasks (job_id, relays, kind, params)
		VALUES ($1, $2, 'bench', $3) RETURNING id`
	selectBenchParams = `SELECT coalesce(params, '{}'::jsonb) FROM entry_probe_tasks WHERE id = $1`
	selectBenchTask   = `
		SELECT status, result, params FROM entry_probe_tasks WHERE id = $1 AND kind = 'bench'`
)

// QueueBench asks a vantage to bench the box through one of its relays for
// `d` — beside a relay of `control` at the same time, when control is not ""
// (the canary's comparison box, CompareWithControl). job ties it to the
// rollout job that waits on it (uuid.Nil: none).
func (s *Service) QueueBench(ctx context.Context, job uuid.UUID, box, control, phase string, d time.Duration) (uuid.UUID, error) {
	if d < MinBenchDuration || d > MaxBenchDuration {
		return uuid.Nil, fmt.Errorf("%w: bench of %s, want %s to %s", ErrRejected, d, MinBenchDuration, MaxBenchDuration)
	}
	switch phase {
	case BenchBefore, BenchAfter, BenchManual:
	default:
		return uuid.Nil, fmt.Errorf("%w: bench phase %q", ErrRejected, phase)
	}
	var p benchParams
	var relay string
	err := s.pool.QueryRow(ctx, benchRelayOfBox, box).Scan(&relay, &p.Arch, &p.EdgeSHA)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("%w: box %s has no deployed relay to bench through", ErrNoSuchRelay, box)
	}
	if err != nil {
		return uuid.Nil, err
	}
	relays := []string{relay}
	if control != "" {
		var crelay, carch string
		err := s.pool.QueryRow(ctx, benchRelayOfBox, control).Scan(&crelay, &carch, &p.ControlEdgeSHA)
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, fmt.Errorf("%w: control box %s has no deployed relay", ErrNoSuchRelay, control)
		}
		if err != nil {
			return uuid.Nil, err
		}
		relays, p.Control, p.ControlRelay = append(relays, crelay), control, crelay
	}
	p.DurationSec, p.Phase = int(d/time.Second), phase
	raw, err := json.Marshal(p)
	if err != nil {
		return uuid.Nil, err
	}
	var jobID any
	if job != uuid.Nil {
		jobID = job
	}
	var id uuid.UUID
	err = s.pool.QueryRow(ctx, insertBenchTask, jobID, relays, raw).Scan(&id)
	return id, err
}

// BenchOutcome returns a bench's result, or nil while it is not done.
func (s *Service) BenchOutcome(ctx context.Context, id uuid.UUID) (*BenchResult, error) {
	var status string
	var res, params []byte
	err := s.pool.QueryRow(ctx, selectBenchTask, id).Scan(&status, &res, &params)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: bench %s", ErrNoSuchJob, id)
	}
	if err != nil || status != "done" {
		return nil, err
	}
	var r BenchResult
	if err := json.Unmarshal(res, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// WaitBench waits for a bench to finish: its duration plus `slack` for a
// vantage to claim it and the hub to hear back. A vantage that never answers
// is a result too — the run did not happen.
func (s *Service) WaitBench(ctx context.Context, id uuid.UUID, d, slack, poll time.Duration) (BenchResult, error) {
	deadline := time.Now().Add(d + slack)
	for {
		r, err := s.BenchOutcome(ctx, id)
		if err != nil {
			return BenchResult{}, err
		}
		if r != nil {
			return *r, nil
		}
		if time.Now().After(deadline) {
			return BenchResult{Error: "ни одна точка обзора не прогнала бенч вовремя"}, nil
		}
		select {
		case <-ctx.Done():
			return BenchResult{}, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// recordBench sends a finished run to the dataset. Failures are logged: the
// row in Postgres is what decides.
func (s *Service) recordBench(ctx context.Context, id uuid.UUID, relay, region string, r BenchResult) {
	if s.benches == nil {
		return
	}
	var raw []byte
	if err := s.pool.QueryRow(ctx, selectBenchParams, id).Scan(&raw); err != nil {
		applog.Error(ctx, "entry", "bench params unreadable", applog.F("bench", id), applog.F("err", err.Error()))
		return
	}
	var p benchParams
	_ = json.Unmarshal(raw, &p)
	canary := r
	canary.Control = nil
	rows := []BenchRecord{{
		At: s.now().UTC(), TaskID: id, Region: region, Relay: relay, Box: boxOfRelay(relay),
		Arch: p.Arch, EdgeSHA: p.EdgeSHA, Phase: p.Phase, Result: canary,
	}}
	if r.Control != nil && p.Control != "" {
		rows = append(rows, BenchRecord{
			At: rows[0].At, TaskID: id, Region: region, Relay: p.ControlRelay, Box: p.Control,
			Arch: p.Arch, EdgeSHA: p.ControlEdgeSHA, Phase: "control-" + p.Phase, Result: *r.Control,
		})
	}
	for _, rec := range rows {
		if err := s.benches.InsertEntryBench(ctx, rec); err != nil {
			applog.Error(ctx, "entry", "bench not written to the dataset", applog.F("bench", id), applog.F("err", err.Error()))
		}
	}
}

// boxOfRelay is the box a relay name says it runs on (<box>-<n>).
func boxOfRelay(relay string) string {
	for i := len(relay) - 1; i >= 0; i-- {
		if relay[i] == '-' {
			return relay[:i]
		}
	}
	return relay
}
