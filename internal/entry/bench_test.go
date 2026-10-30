package entry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Roadmap 42 П6.14 Ф3: benches of a relay build.

// benchPolicyUnderTest is what the thresholds used to be as Go constants
// (migration 067 holds them now). One pair, because these tests judge the
// comparison itself; how many pairs feed it is the canary's business.
func benchPolicyUnderTest() BenchPolicy {
	return BenchPolicy{MinThroughputShare: 0.8, MaxRTTGrowth: 1.3, Pairs: 1, RunDuration: 5 * time.Minute}
}

func TestCompareBenchHoldsTheOwnersThresholds(t *testing.T) {
	base := BenchResult{ChatN: 100, MediaN: 20, DownMbps: 50, UpMbps: 20, ChatRTTP50Ms: 100}
	with := func(f func(*BenchResult)) BenchResult { r := base; f(&r); return r }
	cases := []struct {
		name  string
		after BenchResult
		ok    bool
		why   string
	}{
		{"the same", base, true, ""},
		{"20% slower down is still fine", with(func(r *BenchResult) { r.DownMbps = 40 }), true, ""},
		{"more than 20% slower down", with(func(r *BenchResult) { r.DownMbps = 39.9 }), false, "приём"},
		{"more than 20% slower up", with(func(r *BenchResult) { r.UpMbps = 15.9 }), false, "отдача"},
		{"rtt +30% is still fine", with(func(r *BenchResult) { r.ChatRTTP50Ms = 130 }), true, ""},
		{"rtt over +30%", with(func(r *BenchResult) { r.ChatRTTP50Ms = 131 }), false, "rtt"},
		{"one drop", with(func(r *BenchResult) { r.Drops = 1 }), false, "обрывов: 1"},
		{"did not run", BenchResult{Error: "туннель"}, false, "не состоялся"},
		{"ran empty", with(func(r *BenchResult) { r.MediaN = 0 }), false, "пустой"},
	}
	for _, c := range cases {
		ok, reasons, err := CompareBench(base, c.after, benchPolicyUnderTest())
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if ok != c.ok || (c.why != "" && !strings.Contains(strings.Join(reasons, "; "), c.why)) {
			t.Fatalf("%s: ok=%v reasons=%v", c.name, ok, reasons)
		}
	}
	// A before run that broke or never ran says nothing about the build.
	for _, before := range []BenchResult{{Error: "x"}, with(func(r *BenchResult) { r.Drops = 2 }), {}} {
		if _, _, err := CompareBench(before, base, benchPolicyUnderTest()); !errors.Is(err, ErrBenchBaseline) {
			t.Fatalf("baseline %+v: %v", before, err)
		}
	}
}

type benchSinkFake struct {
	mu   sync.Mutex
	rows []BenchRecord
}

func (f *benchSinkFake) InsertEntryBench(_ context.Context, r BenchRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, r)
	return nil
}

func TestABenchIsQueuedClaimedApartAndReported(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sink := &benchSinkFake{}
	h.svc.SetBenchSink(sink)
	relay := h.personal(1)[0]
	box := boxOf(relay)
	sha := sha256Hex([]byte("edge-v1"))
	if err := h.svc.setBoxEdge(ctx, box, "amd64", sha); err != nil {
		t.Fatal(err)
	}

	id, err := h.svc.QueueBench(ctx, uuid.Nil, box, "", BenchBefore, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// The admission loop never sees it: it must not wait behind a bench, nor
	// run one with an admission probe's patience.
	for {
		task, err := h.svc.ClaimProbeTask(ctx, testVantage, TaskAdmission)
		if err != nil {
			t.Fatal(err)
		}
		if task == nil {
			break
		}
		if task.ID == id {
			t.Fatal("the admission loop claimed a bench")
		}
		// Someone else's leftover: answered as its claimer, about its own
		// relays, with "no evidence" — a finish that answers nothing is
		// refused now (checkAnswer).
		left := map[string]string{}
		for _, r := range task.Relays {
			left[r.Name] = VerdictUnknown
		}
		if err := h.svc.FinishProbeTask(ctx, task.ID, testVantage, left, nil); err != nil {
			t.Fatalf("answer a leftover: %v", err)
		}
	}
	var task *ProbeTask
	for task == nil || task.ID != id {
		if task, err = h.svc.ClaimProbeTask(ctx, testVantage, TaskBench); err != nil || task == nil {
			t.Fatalf("bench claim: %v %v", task, err)
		}
	}
	var params struct {
		DurationSec int    `json:"durationSec"`
		Phase       string `json:"phase"`
	}
	_ = json.Unmarshal(task.Params, &params)
	if len(task.Relays) != 1 || task.Relays[0].Name != relay || task.Relays[0].UUID == "" ||
		params.DurationSec != 120 || params.Phase != BenchBefore {
		t.Fatalf("claimed %+v params %+v", task.Relays, params)
	}
	if r, _ := h.svc.BenchOutcome(ctx, id); r != nil {
		t.Fatal("an unfinished bench has an outcome")
	}

	// A bench comes back with its result, or not at all.
	if err := h.svc.FinishProbeTask(ctx, id, testVantage, map[string]string{relay: "benched"}, nil); !errors.Is(err, ErrRejected) {
		t.Fatalf("a bench without a result: %v", err)
	}
	res := BenchResult{DurationS: 120, ChatN: 40, MediaN: 8, DownMbps: 42, UpMbps: 11, ChatRTTP50Ms: 61}
	if err := h.svc.FinishProbeTask(ctx, id, testVantage, map[string]string{relay: "benched"}, &res); err != nil {
		t.Fatal(err)
	}
	got, err := h.svc.WaitBench(ctx, id, 0, time.Second, 10*time.Millisecond)
	if err != nil || got != res {
		t.Fatalf("outcome %+v, %v", got, err)
	}
	if len(sink.rows) != 1 {
		t.Fatalf("dataset rows %d", len(sink.rows))
	}
	row := sink.rows[0]
	if row.Box != box || row.Relay != relay || row.Region != "ru-test" || row.Phase != BenchBefore ||
		row.EdgeSHA != sha || row.Arch != "amd64" || row.Result != res {
		t.Fatalf("dataset row %+v", row)
	}
}

func TestAnAdmissionProbeCarriesNoResult(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	box, _ := ph.harness.box()
	relay := ph.relay(box, KindPersonal)
	job, err := ph.svc.startJob(ctx, box, "")
	if err != nil {
		t.Fatal(err)
	}
	defer ph.svc.finishJob(ctx, job, nil) //nolint:errcheck // frees the box
	task, err := ph.svc.createProbeTask(ctx, job, []string{relay})
	if err != nil {
		t.Fatal(err)
	}
	for {
		c, err := ph.svc.ClaimProbeTask(ctx, testVantage, TaskAdmission)
		if err != nil || c == nil {
			t.Fatalf("claim: %v %v", c, err)
		}
		if c.ID == task {
			break
		}
	}
	if err := ph.svc.FinishProbeTask(ctx, task, testVantage, map[string]string{relay: "clean"}, &BenchResult{}); !errors.Is(err, ErrRejected) {
		t.Fatalf("an admission probe with a bench result: %v", err)
	}
	if err := ph.svc.FinishProbeTask(ctx, task, testVantage, map[string]string{relay: "clean"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestQueueBenchRules(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	box, _ := h.box()
	if _, err := h.svc.QueueBench(ctx, uuid.Nil, box, "", BenchManual, time.Minute); !errors.Is(err, ErrNoSuchRelay) {
		t.Fatalf("a box with nothing deployed: %v", err)
	}
	h.relay(box, KindPersonal) // pending: never admitted, not a path anyone uses
	if _, err := h.svc.QueueBench(ctx, uuid.Nil, box, "", BenchManual, time.Minute); !errors.Is(err, ErrNoSuchRelay) {
		t.Fatalf("a box with only a pending relay: %v", err)
	}
	for _, d := range []time.Duration{30 * time.Second, 11 * time.Minute} {
		if _, err := h.svc.QueueBench(ctx, uuid.Nil, box, "", BenchManual, d); !errors.Is(err, ErrRejected) {
			t.Fatalf("duration %s: %v", d, err)
		}
	}
	if _, err := h.svc.QueueBench(ctx, uuid.Nil, box, "", "sometime", time.Minute); !errors.Is(err, ErrRejected) {
		t.Fatalf("phase: %v", err)
	}
	// The canary's relays are out of the pool while it is benched — it is
	// still the path to measure.
	alive := h.personal(1)[0]
	if _, _, err := h.svc.Drain(ctx, boxOf(alive), StatusTesting); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.QueueBench(ctx, uuid.Nil, boxOf(alive), "", BenchAfter, time.Minute); err != nil {
		t.Fatalf("bench of a testing box: %v", err)
	}
	// Leave no queued bench behind for other tests' claim loops.
	if _, err := h.pool.Exec(ctx, `DELETE FROM entry_probe_tasks WHERE kind = 'bench' AND status = 'queued'`); err != nil {
		t.Fatal(err)
	}
}

// tokVantage is the token lookup with one slot: "tok" is the test vantage's.
type tokVantage struct{}

func (tokVantage) VantageByToken(_ context.Context, token string) (string, error) {
	if token == "tok" {
		return testVantage, nil
	}
	return "", ErrNoSuchVantage
}

func benchServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	RegisterBenchRoutes(mux, tokVantage{})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func benchReq(t *testing.T, srv *httptest.Server, method, path, token string, body []byte) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+benchBase+path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func TestTheBenchRoutesAnswerTheProberOnly(t *testing.T) {
	srv := benchServer(t)
	if code, _ := benchReq(t, srv, "POST", "/echo?reply=10", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	if code, body := benchReq(t, srv, "POST", "/echo?reply=1500", "tok", make([]byte, 700)); code != 200 || len(body) != 1500 {
		t.Fatalf("echo: %d, %d bytes", code, len(body))
	}
	if code, _ := benchReq(t, srv, "POST", "/echo?reply=70000", "tok", nil); code != http.StatusBadRequest {
		t.Fatalf("echo past a chat message: %d", code)
	}
	code, blob := benchReq(t, srv, "GET", "/blob?size=3145728", "tok", nil)
	if code != 200 || len(blob) != 3<<20 {
		t.Fatalf("blob: %d, %d bytes", code, len(blob))
	}
	// Random, not filler: a compressible payload would measure the wrong thing.
	if bytes.Count(blob, []byte{0}) > len(blob)/100 {
		t.Fatal("the blob is mostly zeros")
	}
	if code, _ := benchReq(t, srv, "GET", "/blob?size=9000000", "tok", nil); code != http.StatusBadRequest {
		t.Fatalf("blob past a photo: %d", code)
	}
	if code, body := benchReq(t, srv, "POST", "/sink", "tok", make([]byte, 1<<20)); code != 200 || !strings.Contains(string(body), "1048576") {
		t.Fatalf("sink: %d %s", code, body)
	}

	ws := "ws" + strings.TrimPrefix(srv.URL, "http") + benchBase + "/stream"
	if _, _, err := websocket.DefaultDialer.Dial(ws, nil); err == nil {
		t.Fatal("the stream opened without a token")
	}
	conn, _, err := websocket.DefaultDialer.Dial(ws, http.Header{"Authorization": {"Bearer tok"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	frame := []byte("voice-frame-120-bytes")
	if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, got, err := conn.ReadMessage(); err != nil || !bytes.Equal(got, frame) {
		t.Fatalf("echo %q, %v", got, err)
	}
	// A frame bigger than voice ends the stream.
	_ = conn.WriteMessage(websocket.BinaryMessage, make([]byte, 4096))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("an oversized frame was echoed")
	}
}

// TestPoolingPairsOutvotesOneSlowMinute is the П6.14 lesson of the stand: the
// same unchanged binary measured 15.4 … 63.4 Mbit/s over a week, so a verdict
// resting on one pair is a verdict about the hour. Three pairs, one of them a
// dip, must still read as "no worse".
func TestPoolingPairsOutvotesOneSlowMinute(t *testing.T) {
	pol := benchPolicyUnderTest()
	pol.Pairs = 3
	run := func(down float64) BenchResult {
		return BenchResult{ChatN: 100, MediaN: 20, DownMbps: down, UpMbps: 25, ChatRTTP50Ms: 80}
	}
	// The numbers are the stand's own, 24.10: the old build twice, the new
	// build with one dip among its runs.
	befores := []BenchResult{run(55.7), run(63.4), run(60.2)}
	afters := []BenchResult{run(30.2), run(62.0), run(59.0)}
	pb, pa := PoolRuns(befores), PoolRuns(afters)
	ok, reasons, err := CompareBench(pb, pa, pol)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("three pairs still condemned the build: %v (до %.1f, после %.1f)", reasons, pb.DownMbps, pa.DownMbps)
	}
	// And one pair alone, the setting that stalled the real rollout, does not.
	if ok, _, _ := CompareBench(befores[0], afters[0], pol); ok {
		t.Fatal("one pair read the dip as fine — then the averaging proves nothing")
	}
	// A build that really is slower is still caught, however many pairs.
	half := []BenchResult{run(28), run(30), run(29)}
	if ok, _, _ := CompareBench(pb, PoolRuns(half), pol); ok {
		t.Fatal("a build at half the throughput passed")
	}
	if e := PoolRuns([]BenchResult{run(60), {Error: "x"}}); e.Error == "" {
		t.Fatal("a failed run pooled into a good one")
	}
	if e := PoolRuns(nil); e.Error == "" {
		t.Fatal("no runs at all is not a verdict")
	}
}

// TestPoolingAveragesTheControlToo: a canary judged against a control box is
// judged on a ratio, and a ratio whose halves were measured over different
// minutes is the very drift the control exists to cancel.
func TestPoolingAveragesTheControlToo(t *testing.T) {
	with := func(down, ctl float64) BenchResult {
		r := BenchResult{ChatN: 100, MediaN: 20, DownMbps: down, UpMbps: 25, ChatRTTP50Ms: 80}
		c := r
		c.DownMbps = ctl
		r.Control = &c
		return r
	}
	pooled := PoolRuns([]BenchResult{with(60, 50), with(30, 25)})
	if pooled.DownMbps != 45 {
		t.Fatalf("canary side pooled to %.1f", pooled.DownMbps)
	}
	if pooled.Control == nil || pooled.Control.DownMbps != 37.5 {
		t.Fatalf("control side pooled to %+v", pooled.Control)
	}
	// A run without a control leaves the pooled result without one: half a
	// ratio is not a ratio.
	bare := BenchResult{ChatN: 1, MediaN: 1, DownMbps: 10, UpMbps: 1, ChatRTTP50Ms: 10}
	if p := PoolRuns([]BenchResult{with(60, 50), bare}); p.Control != nil {
		t.Fatal("a control appeared out of a run that had none")
	}
}

func TestCompareWithControlCancelsDriftOnTheSharedPath(t *testing.T) {
	run := func(down, rtt float64) BenchResult {
		return BenchResult{ChatN: 50, MediaN: 10, DownMbps: down, UpMbps: 20, ChatRTTP50Ms: rtt, VoiceSent: 15000}
	}
	pair := func(c, k BenchResult) BenchResult { c.Control = &k; return c }
	before := pair(run(63.5, 84), run(60, 80))

	// The stand's evening: the path fell to 8 Mbit/s and 121 ms for both.
	ok, why, err := CompareWithControl(before, pair(run(8.1, 121), run(7.9, 118)), benchPolicyUnderTest())
	if err != nil || !ok {
		t.Fatalf("drift on the shared path condemned the build: %v %v", why, err)
	}
	// The canary alone fell: the build.
	if ok, why, _ := CompareWithControl(before, pair(run(8.1, 84), run(60, 80)), benchPolicyUnderTest()); ok || !strings.Contains(strings.Join(why, ";"), "приём") {
		t.Fatalf("a canary that lost ground was passed: %v", why)
	}
	if ok, why, _ := CompareWithControl(before, pair(run(63, 140), run(60, 80)), benchPolicyUnderTest()); ok || !strings.Contains(strings.Join(why, ";"), "rtt") {
		t.Fatalf("a canary whose rtt grew alone was passed: %v", why)
	}
	broke := run(63, 84)
	broke.Drops = 2
	if ok, _, _ := CompareWithControl(before, pair(broke, run(60, 80)), benchPolicyUnderTest()); ok {
		t.Fatal("canary drops with a clean control were passed")
	}
	// The control broke: the path, not the build.
	ctlBroke := run(60, 80)
	ctlBroke.Drops = 3
	if _, _, err := CompareWithControl(before, pair(run(63, 84), ctlBroke), benchPolicyUnderTest()); !errors.Is(err, ErrBenchUnsteady) {
		t.Fatalf("a broken control: %v", err)
	}
	if _, _, err := CompareWithControl(run(63, 84), before, benchPolicyUnderTest()); !errors.Is(err, ErrBenchBaseline) {
		t.Fatalf("a before run with no control: %v", err)
	}
}

func TestSteadyReadsTheVoiceStream(t *testing.T) {
	r := BenchResult{VoiceSent: 15000, VoiceLate: 70}
	if why := Steady(r); why != "" {
		t.Fatalf("0.47%% late is steady: %s", why)
	}
	r.VoiceLate = 841 // the stand's evening
	if Steady(r) == "" {
		t.Fatal("841 late frames counted as steady")
	}
	if Steady(BenchResult{}) == "" || Steady(BenchResult{Error: "x"}) == "" {
		t.Fatal("a run with no voice stream counted as steady")
	}
}
