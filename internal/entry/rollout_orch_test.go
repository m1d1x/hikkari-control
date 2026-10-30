package entry

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/m1d1x/hikkari-control/internal/sshexec"
)

// Roadmap 42 П6.14 Ф4: the orchestrator against a fleet of fake boxes, one
// per address, on the live test database.

// fakeFleet is a fake box per address.
type fakeFleet struct {
	mu    sync.Mutex
	boxes map[string]*fakeBox
}

func (f *fakeFleet) dial(ctx context.Context, t sshexec.Target, auth ...ssh.AuthMethod) (Remote, error) {
	f.mu.Lock()
	b := f.boxes[t.Addr]
	if b == nil {
		b = &fakeBox{password: "provider-pw-123", hostKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFake" + t.Addr}
		f.boxes[t.Addr] = b
	}
	f.mu.Unlock()
	return b.dial(ctx, t, auth...)
}

func (f *fakeFleet) box(t *testing.T, h *harness, name string) *fakeBox {
	t.Helper()
	acc, err := h.svc.boxAccess(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.boxes[boxTarget(acc).Addr]
}

func boxTarget(acc boxAccess) sshexec.Target {
	return sshexec.Target{Addr: net.JoinHostPort(acc.address, "22")}
}

// benchVerdict says what a vantage measures for a bench of `phase` on `box`.
type benchVerdict func(box, phase string) BenchResult

type rolloutHarness struct {
	*harness
	fleet  *fakeFleet
	v1, v2 *Provisioner
	sha1   string
	sha2   string
	stop   context.CancelFunc
	bench  benchVerdict
	probe  func(relay string) string
	mu     sync.Mutex
	phases []string // bench phases the vantage ran, in order
	// pairs is the bench policy's sample size for this harness (migration
	// 067). One by default, because most of these tests judge a stage of the
	// rollout and not the averaging; the tests that are about the averaging
	// set it themselves.
	pairs int
}

var goodRun = BenchResult{ChatN: 50, MediaN: 10, DownMbps: 60, UpMbps: 25, ChatRTTP50Ms: 80, VoiceSent: 15000}

func buildDir(t *testing.T, content []byte) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "linux-amd64"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "linux-amd64", "nekko-edge"), content, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func newRolloutHarness(t *testing.T) *rolloutHarness {
	h := newHarness(t)
	ctx := context.Background()
	// The fleet is every box with a live relay in the shared test database:
	// take whatever earlier runs left out of it, and close their rollouts.
	if _, err := h.pool.Exec(ctx, `UPDATE entry_relays SET status = 'dead', died_at = now(), died_reason = 'other' WHERE status <> 'dead'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE entry_rollouts SET stage = 'stopped', error = 'test', finished_at = now()
		WHERE stage IN ('canary', 'rolling')`); err != nil {
		t.Fatal(err)
	}
	fleet := &fakeFleet{boxes: map[string]*fakeBox{}}
	run := uuid.NewString()
	b1, b2 := []byte("edge-v1-"+run), []byte("edge-v2-"+run)
	rh := &rolloutHarness{
		harness: h, fleet: fleet,
		v1:   NewProvisioner(h.svc, fleet.dial, buildDir(t, b1)),
		v2:   NewProvisioner(h.svc, fleet.dial, buildDir(t, b2)),
		sha1: sha256Hex(b1), sha2: sha256Hex(b2),
		bench: func(string, string) BenchResult { return goodRun },
		probe: func(string) string { return "clean" },
	}
	for _, p := range []*Provisioner{rh.v1, rh.v2} {
		p.probeTimeout, p.probePoll, p.drainPoll = 10*time.Second, 20*time.Millisecond, time.Millisecond
	}
	vctx, stop := context.WithCancel(ctx)
	rh.stop = stop
	t.Cleanup(stop)
	go rh.vantage(vctx)
	return rh
}

// vantage answers admission probes and benches until stopped.
func (rh *rolloutHarness) vantage(ctx context.Context) {
	for ctx.Err() == nil {
		did := false
		if task, _ := rh.svc.ClaimProbeTask(ctx, testVantage, TaskAdmission); task != nil {
			v := map[string]string{}
			for _, r := range task.Relays {
				v[r.Name] = rh.probe(r.Name)
			}
			_ = rh.svc.FinishProbeTask(ctx, task.ID, testVantage, v, nil)
			did = true
		}
		if task, _ := rh.svc.ClaimProbeTask(ctx, testVantage, TaskBench); task != nil {
			var p benchParams
			_ = json.Unmarshal(task.Params, &p)
			relay := task.Relays[0].Name
			res := rh.bench(boxOfRelay(relay), p.Phase)
			rh.mu.Lock()
			rh.phases = append(rh.phases, boxOfRelay(relay)+":"+p.Phase)
			rh.mu.Unlock()
			verdicts := map[string]string{relay: "benched"}
			if len(task.Relays) == 2 {
				ctl := rh.bench(boxOfRelay(task.Relays[1].Name), "control-"+p.Phase)
				res.Control = &ctl
				// A bench answers for every relay it ran through, as the
				// prober does (checkAnswer).
				verdicts[task.Relays[1].Name] = "benched"
			}
			_ = rh.svc.FinishProbeTask(ctx, task.ID, testVantage, verdicts, &res)
			did = true
		}
		if !did {
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// boxOnV1 provisions a box with the old build and returns its name.
func (rh *rolloutHarness) boxOnV1() string {
	rh.t.Helper()
	u := uuid.New()
	name := boxName("r", u)
	id, err := rh.v1.CreateAndProvision(context.Background(), NewBox{
		BoxSpec:  BoxSpec{Name: name, Address: "2001:db8:" + u.String()[:4] + "::1", Provider: "vultr", Location: "amsterdam"},
		Password: "provider-pw-123",
	}, "")
	if err != nil {
		rh.t.Fatal(err)
	}
	rh.made = append(rh.made, name+"-1")
	for i := 0; i < 400; i++ {
		j, _ := rh.svc.Job(context.Background(), id)
		if j.Status == "done" {
			return name
		}
		if j.Status == "failed" {
			rh.t.Fatalf("provision %s: %s", name, j.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	rh.t.Fatal("provision did not finish")
	return ""
}

func (rh *rolloutHarness) orchestrator() *Orchestrator {
	o := NewOrchestrator(rh.svc, rh.v2)
	o.benchSlack, o.benchPoll, o.drainFor = 5*time.Second, 10*time.Millisecond, 0
	// How long a run lasts and how many pairs a verdict needs are the bench
	// policy's (migration 067), so the harness turns those knobs in the row
	// rather than on the orchestrator: one pair of one-minute runs, which is
	// what these tests were written against.
	pairs, oneMinute := rh.pairs, 60
	if pairs == 0 {
		pairs = 1
	}
	if _, err := rh.svc.SaveBenchPolicy(context.Background(), BenchPolicyPatch{
		Pairs: &pairs, RunSeconds: &oneMinute,
	}, "test"); err != nil {
		rh.t.Fatalf("bench policy: %v", err)
	}
	return o
}

func (rh *rolloutHarness) rolloutOfV2() Rollout {
	rh.t.Helper()
	ro, err := scanRollout(rh.pool.QueryRow(context.Background(), rolloutByBuild, "amd64", rh.sha2))
	if err != nil {
		rh.t.Fatalf("rollout of v2: %v", err)
	}
	return ro
}

func (rh *rolloutHarness) statuses(box string) []string {
	rh.t.Helper()
	rows, err := rh.pool.Query(context.Background(), `SELECT status FROM entry_relays WHERE box = $1 ORDER BY name`, box)
	if err != nil {
		rh.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func (rh *rolloutHarness) recorded(box string) string {
	rh.t.Helper()
	var sha string
	_ = rh.pool.QueryRow(context.Background(), `SELECT coalesce(edge_sha, '') FROM entry_boxes WHERE name = $1`, box).Scan(&sha)
	return sha
}

func (rh *rolloutHarness) runsOn(box string) string {
	rh.t.Helper()
	return sha256Hex(rh.fleet.box(rh.t, rh.harness, box).binary)
}

func TestARolloutTriesTheNewestBoxFirstThenTheRest(t *testing.T) {
	rh := newRolloutHarness(t)
	older := rh.boxOnV1()
	newer := rh.boxOnV1()
	rh.orchestrator().round(context.Background())

	ro := rh.rolloutOfV2()
	if ro.Stage != RolloutDone || ro.CanaryBox != newer {
		t.Fatalf("rollout %+v, want done with canary %s", ro, newer)
	}
	for _, box := range []string{older, newer} {
		if got := rh.runsOn(box); got != rh.sha2 {
			t.Fatalf("%s runs %s…, want v2", box, got[:12])
		}
		st := rh.statuses(box)
		if len(st) != relayTarget(ProtoVlessRealityVision) {
			t.Fatalf("%s relays %v after the rollout", box, st)
		}
		for _, s := range st {
			if s != StatusAlive {
				t.Fatalf("%s relays %v after the rollout", box, st)
			}
		}
		if sha := rh.recorded(box); sha != rh.sha2 {
			t.Fatalf("%s recorded at %s", box, sha)
		}
	}
	// Only the canary is benched, before and after; the rest is probed.
	if got := strings.Join(rh.phases, " "); got != newer+":before "+newer+":after" {
		t.Fatalf("benches %q, newer %s older %s", got, newer, older)
	}
	// One job per box, of the right kind, under the rollout.
	var kinds []string
	rows, _ := rh.pool.Query(context.Background(),
		`SELECT box || ':' || kind || ':' || status FROM entry_jobs WHERE rollout_id = $1 ORDER BY created_at`, ro.ID)
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		kinds = append(kinds, k)
	}
	rows.Close()
	if got := strings.Join(kinds, " "); got != newer+":canary:done "+older+":update:done" {
		t.Fatalf("jobs %q", got)
	}

	// Run again: nothing behind, nothing to do.
	rh.orchestrator().round(context.Background())
	if n := len(rh.phases); n != 2 {
		t.Fatalf("a second run benched again (%d)", n)
	}
}

func TestAWorseBuildIsRolledBackAndNeverRetried(t *testing.T) {
	rh := newRolloutHarness(t)
	other := rh.boxOnV1()
	canary := rh.boxOnV1()
	rh.bench = func(_, phase string) BenchResult {
		if phase == BenchAfter {
			r := goodRun
			r.DownMbps = 30 // half the throughput
			return r
		}
		return goodRun
	}
	rh.orchestrator().round(context.Background())

	ro := rh.rolloutOfV2()
	if ro.Stage != RolloutFailed || !strings.Contains(ro.Error, "приём") {
		t.Fatalf("rollout %+v", ro)
	}
	if got := rh.runsOn(canary); got != rh.sha1 {
		t.Fatalf("the canary was not rolled back: runs %s…", got[:12])
	}
	if got := rh.runsOn(other); got != rh.sha1 {
		t.Fatal("a box past the canary was touched")
	}
	if st := rh.statuses(canary); st[0] != "alive" {
		t.Fatalf("the canary's relays are %v after the rollback", st)
	}
	// Two boxes: judged against the control, which held at 60 while the
	// canary fell to 30 — one pair settles it, no ABAB.
	if !strings.Contains(ro.Error, "относительно контроля") {
		t.Fatalf("not judged against the control: %s", ro.Error)
	}
	if n := len(rh.phases); n != 2 {
		t.Fatalf("%d benches, want one pair", n)
	}
	// The next hub start leaves the bad build alone.
	rh.orchestrator().round(context.Background())
	if n := len(rh.phases); n != 2 {
		t.Fatalf("the failed build was tried again (%d benches)", n)
	}
}

func TestABaselineThatCannotBeMeasuredIsNoVerdict(t *testing.T) {
	rh := newRolloutHarness(t)
	box := rh.boxOnV1()
	rh.bench = func(string, string) BenchResult { return BenchResult{Error: "туннель не провёз"} }
	rh.orchestrator().round(context.Background())

	ro := rh.rolloutOfV2()
	if ro.Stage != RolloutCanary || ro.CanaryBox != "" {
		t.Fatalf("rollout %+v, want still a canary to try", ro)
	}
	if rh.runsOn(box) != rh.sha1 || rh.statuses(box)[0] != "alive" {
		t.Fatal("an unmeasured canary was swapped or left out of the pool")
	}
	// Once the vantage can measure, the same build goes through.
	rh.bench = func(string, string) BenchResult { return goodRun }
	rh.orchestrator().round(context.Background())
	if ro := rh.rolloutOfV2(); ro.Stage != RolloutDone {
		t.Fatalf("rollout %+v", ro)
	}
}

func TestAnUpdatedBoxThatProbesDirtyStopsTheRollout(t *testing.T) {
	rh := newRolloutHarness(t)
	older := rh.boxOnV1()
	rh.boxOnV1() // the canary, newest
	rh.probe = func(relay string) string {
		if boxOfRelay(relay) == older {
			return "ip_banned"
		}
		return "clean"
	}
	rh.orchestrator().round(context.Background())
	ro := rh.rolloutOfV2()
	if ro.Stage != RolloutFailed || !strings.Contains(ro.Error, older) {
		t.Fatalf("rollout %+v", ro)
	}
	if rh.runsOn(older) != rh.sha1 {
		t.Fatal("the dirty box was not rolled back")
	}
}

func TestACanaryCutOffByARestartIsRolledBackAndTriedAgain(t *testing.T) {
	rh := newRolloutHarness(t)
	ctx := context.Background()
	box := rh.boxOnV1()
	// What a hub killed mid-canary leaves: the rollout at the canary, the box
	// out of the pool and already on the new build.
	ro, err := rh.svc.rolloutOf(ctx, "amd64", rh.sha2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rh.pool.Exec(ctx, setRolloutBox, ro.ID, box); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rh.svc.Drain(ctx, box, StatusTesting); err != nil {
		t.Fatal(err)
	}
	v2, err := os.ReadFile(rh.v2.builds["amd64"].path)
	if err != nil {
		t.Fatal(err)
	}
	fb := rh.fleet.box(t, rh.harness, box)
	fb.mu.Lock()
	fb.prev, fb.binary = fb.binary, v2
	fb.mu.Unlock()

	rh.orchestrator().round(ctx)
	if got := rh.rolloutOfV2(); got.Stage != RolloutDone {
		t.Fatalf("rollout %+v", got)
	}
	// It was benched on the OLD build first: the rollback came before the canary.
	if got := strings.Join(rh.phases, " "); got != box+":before "+box+":after" {
		t.Fatalf("benches %q", got)
	}
	var log string
	_ = rh.pool.QueryRow(ctx, `SELECT string_agg(l.line, ' | ' ORDER BY j.created_at, l.seq)
		FROM entry_jobs j JOIN entry_job_log l ON l.job_id = j.id WHERE j.rollout_id = $1`, ro.ID).Scan(&log)
	if !strings.Contains(log, "откат: на машине снова "+rh.sha1[:12]) {
		t.Fatalf("no rollback in the log: %s", log)
	}
}

func TestAnUnseenBoxIsReadBeforeTheRollout(t *testing.T) {
	rh := newRolloutHarness(t)
	ctx := context.Background()
	box := rh.boxOnV1()
	// A box provisioned before versions were recorded.
	if _, err := rh.pool.Exec(ctx, `UPDATE entry_boxes SET arch = NULL, edge_sha = NULL, edge_seen_at = NULL WHERE name = $1`, box); err != nil {
		t.Fatal(err)
	}
	rh.orchestrator().round(ctx)
	if ro := rh.rolloutOfV2(); ro.Stage != RolloutDone || ro.CanaryBox != box {
		t.Fatalf("rollout %+v", ro)
	}
}

type healthFake map[string]time.Time

func (h healthFake) FailingRelays(context.Context, time.Time, []string) (map[string]time.Time, error) {
	return h, nil
}

func (h healthFake) RelayRTT(context.Context, string, time.Time, time.Time, []string) (float64, int, error) {
	return 0, 0, nil // no history: the path check passes
}

func TestOnlyARelaysOwnFailuresKeepItsBoxFromTheCanary(t *testing.T) {
	rh := newRolloutHarness(t)
	box := rh.boxOnV1()
	relay := box + "-1"

	// A failure from before the relay was born is a predecessor's under the
	// same name: no reason to skip the box.
	o := rh.orchestrator()
	o.SetHealth(healthFake{relay: time.Now().Add(-48 * time.Hour)})
	o.round(context.Background())
	if ro := rh.rolloutOfV2(); ro.Stage != RolloutDone || ro.CanaryBox != box {
		t.Fatalf("rollout %+v", ro)
	}
}

func TestASickBoxIsNotTheCanary(t *testing.T) {
	rh := newRolloutHarness(t)
	box := rh.boxOnV1()
	o := rh.orchestrator()
	o.SetHealth(healthFake{box + "-1": time.Now()})
	o.round(context.Background())
	ro := rh.rolloutOfV2()
	if ro.Stage != RolloutCanary || ro.CanaryBox != "" || rh.runsOn(box) != rh.sha1 || len(rh.phases) != 0 {
		t.Fatalf("a sick box was tried: %+v, benches %v", ro, rh.phases)
	}
}

func TestAFailedCanaryWhoseBinaryNeverChangedHasNothingToRollBack(t *testing.T) {
	// The stand's first live canary: the box already ran the image's build
	// (its recorded version was stale), the after-bench came out worse, and
	// the rollback looked for a .prev the swap never had reason to keep.
	rh := newRolloutHarness(t)
	ctx := context.Background()
	box := rh.boxOnV1()
	v2, _ := os.ReadFile(rh.v2.builds["amd64"].path)
	fb := rh.fleet.box(t, rh.harness, box)
	fb.mu.Lock()
	fb.binary, fb.prev = v2, nil
	fb.mu.Unlock()
	rh.bench = func(_, phase string) BenchResult {
		r := goodRun
		if phase == BenchAfter {
			r.DownMbps = 30
		}
		return r
	}
	rh.orchestrator().round(ctx)

	ro := rh.rolloutOfV2()
	if ro.Stage != RolloutFailed || strings.Contains(ro.Error, "откат не удался") {
		t.Fatalf("rollout %+v", ro)
	}
	if rh.runsOn(box) != rh.sha2 || rh.statuses(box)[0] != "alive" {
		t.Fatal("the box was touched or left out of the pool")
	}
}

// abab answers the before and after benches in turn with the given downlinks.
func (rh *rolloutHarness) abab(downs ...float64) {
	var mu sync.Mutex
	i := 0
	rh.bench = func(string, string) BenchResult {
		mu.Lock()
		defer mu.Unlock()
		r := goodRun
		r.DownMbps = downs[min(i, len(downs)-1)]
		i++
		return r
	}
}

// TestTwoPairsAverageAwayOneSlowRun: the sample size is what carries a build
// over a dip, and the dip is the stand's own — the same binary measured 30 and
// 62 Mbit/s in one hour. Averaged, 60 against 50 is within the threshold; the
// first pair alone would have condemned it, which is what the real rollout of
// 24cb2790 did.
func TestTwoPairsAverageAwayOneSlowRun(t *testing.T) {
	rh := newRolloutHarness(t)
	rh.pairs = 2
	box := rh.boxOnV1()
	rh.abab(60, 40, 60, 60) // after drops a third once, then holds: noise
	rh.orchestrator().round(context.Background())
	if ro := rh.rolloutOfV2(); ro.Stage != RolloutDone {
		t.Fatalf("rollout %+v", ro)
	}
	if got := strings.Join(rh.phases, " "); got != box+":before "+box+":after "+box+":before "+box+":after" {
		t.Fatalf("benches %q, want both pairs run", got)
	}
	if rh.runsOn(box) != rh.sha2 {
		t.Fatal("the box is not on the new build")
	}
}

// TestPairsThatAgreeStillFailTheBuild is the other half, and the reason
// throughput keeps its vote: a build that is really slower is slower in every
// pair, and averaging does not hide it.
func TestPairsThatAgreeStillFailTheBuild(t *testing.T) {
	rh := newRolloutHarness(t)
	rh.pairs = 2
	box := rh.boxOnV1()
	rh.abab(60, 40, 60, 40)
	rh.orchestrator().round(context.Background())
	ro := rh.rolloutOfV2()
	if ro.Stage != RolloutFailed || !strings.Contains(ro.Error, "приём") {
		t.Fatalf("rollout %+v", ro)
	}
	if rh.runsOn(box) != rh.sha1 {
		t.Fatal("not rolled back after the pairs disagreed with the build")
	}
}

func TestAPathThatDriftsUnderBothBoxesIsNoReasonToFail(t *testing.T) {
	rh := newRolloutHarness(t)
	rh.boxOnV1()           // the control
	canary := rh.boxOnV1() // newest
	// The stand's evening, under both: the after runs fall together.
	rh.bench = func(_, phase string) BenchResult {
		r := goodRun
		if strings.HasSuffix(phase, BenchAfter) {
			r.DownMbps, r.ChatRTTP50Ms = 8, 121
		}
		return r
	}
	rh.orchestrator().round(context.Background())
	if ro := rh.rolloutOfV2(); ro.Stage != RolloutDone || ro.CanaryBox != canary {
		t.Fatalf("rollout %+v", ro)
	}
}

func TestALoneCanaryOnAnUnsteadyPathIsNoVerdict(t *testing.T) {
	rh := newRolloutHarness(t)
	box := rh.boxOnV1()
	// The before run of the stand's evening: 841 late voice frames.
	rh.bench = func(string, string) BenchResult {
		r := goodRun
		r.VoiceLate = 841
		return r
	}
	rh.orchestrator().round(context.Background())
	ro := rh.rolloutOfV2()
	if ro.Stage != RolloutCanary || ro.CanaryBox != "" {
		t.Fatalf("rollout %+v, want still a canary", ro)
	}
	if rh.runsOn(box) != rh.sha1 || rh.statuses(box)[0] != "alive" {
		t.Fatal("an unjudged canary was swapped or left out")
	}
	// After the swap the path goes bad: no verdict, and the old build back.
	rh.bench = func(_, phase string) BenchResult {
		r := goodRun
		if phase == BenchAfter {
			r.VoiceLate = 900
		}
		return r
	}
	rh.orchestrator().round(context.Background())
	if ro := rh.rolloutOfV2(); ro.Stage != RolloutCanary {
		t.Fatalf("rollout %+v", ro)
	}
	if rh.runsOn(box) != rh.sha1 {
		t.Fatal("an unjudged build was left on the canary")
	}
}

// driftingPath reports a usual RTT of 340 ms and `now` for the last window.
type driftingPath struct{ now float64 }

func (d driftingPath) FailingRelays(context.Context, time.Time, []string) (map[string]time.Time, error) {
	return nil, nil
}

func (d driftingPath) RelayRTT(_ context.Context, _ string, from, to time.Time, _ []string) (float64, int, error) {
	if to.Sub(from) > time.Hour {
		return 340, 288, nil
	}
	return d.now, 6, nil
}

func TestALoneCanaryWaitsOutAPathOffItsUsualLevel(t *testing.T) {
	rh := newRolloutHarness(t)
	box := rh.boxOnV1()
	o := rh.orchestrator()
	o.SetHealth(driftingPath{now: 620}) // the stand, 17:05–18:00
	o.round(context.Background())
	if ro := rh.rolloutOfV2(); ro.Stage != RolloutCanary || len(rh.phases) != 0 {
		t.Fatalf("rollout %+v, benches %v: the canary ran on a drifted path", ro, rh.phases)
	}
	if rh.statuses(box)[0] != "alive" {
		t.Fatal("the box was drained for nothing")
	}
	o.SetHealth(driftingPath{now: 350})
	o.round(context.Background())
	if ro := rh.rolloutOfV2(); ro.Stage != RolloutDone {
		t.Fatalf("rollout %+v once the path settled", ro)
	}
}

func TestNoVerdictIsTriedAgainUntilThereIsOne(t *testing.T) {
	rh := newRolloutHarness(t)
	box := rh.boxOnV1()
	// The first evening is unsteady; an hour later the path is quiet.
	var mu sync.Mutex
	calls := 0
	rh.bench = func(string, string) BenchResult {
		mu.Lock()
		defer mu.Unlock()
		calls++
		r := goodRun
		if calls == 1 {
			r.VoiceLate = 710
		}
		return r
	}
	o := rh.orchestrator()
	o.retryEvery = 20 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	o.Run(ctx) // returns only once the rollout has a verdict
	if ctx.Err() != nil {
		t.Fatal("Run never settled")
	}
	if ro := rh.rolloutOfV2(); ro.Stage != RolloutDone || ro.CanaryBox != box {
		t.Fatalf("rollout %+v", ro)
	}
	if got := strings.Join(rh.phases, " "); got != box+":before "+box+":before "+box+":after" {
		t.Fatalf("benches %q: one unsteady before, then a full pair", got)
	}
}

// coldFirstProbe is the stand on 24.10: a daily baseline from real rounds, and
// a window right after the swap holding a single probe — the service's cold
// first tunnel, 5360 ms against the usual 335.
type coldFirstProbe struct{}

func (coldFirstProbe) FailingRelays(context.Context, time.Time, []string) (map[string]time.Time, error) {
	return nil, nil
}

func (coldFirstProbe) RelayRTT(_ context.Context, _ string, from, to time.Time, _ []string) (float64, int, error) {
	if to.Sub(from) > time.Hour {
		return 335, 288, nil
	}
	return 5360, 1, nil
}

// One probe is not a path. The lone canary compares the relay's recent RTT
// against its daily median, and the window right after a swap holds exactly
// one reading: the cold tunnel of a service that restarted a second ago. Read
// as a verdict, it says the path drifted 16x and throws away a build that is
// fine — which is what happened on the stand.
func TestOneProbeIsNoVerdictOnThePath(t *testing.T) {
	rh := newRolloutHarness(t)
	box := rh.boxOnV1()
	o := rh.orchestrator()
	o.SetHealth(coldFirstProbe{})
	o.round(context.Background())
	if ro := rh.rolloutOfV2(); ro.Stage != RolloutDone {
		t.Fatalf("rollout %+v: a single cold probe must not be read as a drifted path", ro)
	}
	if got := rh.statuses(box)[0]; got != "alive" {
		t.Fatalf("relay left in %q: the box must be back in the pool", got)
	}
}

// A relay drained for a rollout stays under observation. It used to leave the
// prober's list the moment the canary drained its box, which stopped the very
// probes the canary then judged the path by — the rollout blinding itself.
func TestADrainedRelayIsStillWatched(t *testing.T) {
	rh := newRolloutHarness(t)
	box := rh.boxOnV1()
	ctx := context.Background()

	watched := func() []string {
		relays, err := rh.svc.WatchedProbeRelays(ctx)
		if err != nil {
			t.Fatalf("watched relays: %v", err)
		}
		names := make([]string, 0, len(relays))
		for _, r := range relays {
			names = append(names, r.Name)
		}
		return names
	}
	if got := watched(); len(got) != relayTarget(ProtoVlessRealityVision) {
		t.Fatalf("watched %v before the drain", got)
	}
	if _, _, err := rh.svc.Drain(ctx, box, StatusTesting); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if got := rh.statuses(box)[0]; got != StatusTesting {
		t.Fatalf("relay status %q after the drain", got)
	}
	if got := watched(); len(got) != relayTarget(ProtoVlessRealityVision) {
		t.Fatalf("watched %v while drained: the canary's own relay must stay observed", got)
	}
}
