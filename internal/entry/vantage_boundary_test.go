package entry

// The prober → hub boundary (audit 42-autopilot К1, В1, В2, С7): what a
// vantage says is read only as far as the hub can vouch for who says it and
// what it was asked.

import (
	"context"
	"errors"
	"io"
	mrand "math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// openSlot registers an active observation point under a fresh key and
// retires it when the test ends.
func (h *harness) openSlot(prefix string) string {
	h.t.Helper()
	slot := prefix + "-" + uuid.NewString()[:6]
	if _, err := h.svc.CreateVantage(context.Background(), VantageSpec{Slot: slot, Host: "198.51.100.7"}, "test"); err != nil {
		h.t.Fatalf("open slot %s: %v", slot, err)
	}
	h.t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `DELETE FROM entry_vantages WHERE slot = $1`, slot)
	})
	return slot
}

// oldestTask queues a task of kind over relays, older than anything else in
// the queue, so the next claim of that kind takes it.
func (h *harness) oldestTask(kind string, relays []string, params string) uuid.UUID {
	h.t.Helper()
	var p any
	if params != "" {
		p = params
	}
	var id uuid.UUID
	if err := h.pool.QueryRow(context.Background(), `
		INSERT INTO entry_probe_tasks (relays, kind, params, created_at)
		VALUES ($1, $2, $3::jsonb, '2000-01-01') RETURNING id`, relays, kind, p).Scan(&id); err != nil {
		h.t.Fatalf("queue task: %v", err)
	}
	h.t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `DELETE FROM entry_probe_tasks WHERE id = $1`, id)
	})
	return id
}

func (h *harness) taskStatus(id uuid.UUID) string {
	h.t.Helper()
	var s string
	if err := h.pool.QueryRow(context.Background(), `SELECT status FROM entry_probe_tasks WHERE id = $1`, id).Scan(&s); err != nil {
		h.t.Fatal(err)
	}
	return s
}

// К1 (б): a region nobody registered is somebody's word, not an observation
// point. Its verdict must not outvote the registered point's.
func TestAVerdictFromAnUnregisteredRegionCountsForNothing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	slot := h.openSlot("a")
	h.svc.SetVerdicts(&fakeMultiVerdicts{byVantage: map[string]RelayVerdicts{
		slot: {"r1": "clean"},
		"zz": {"r1": "ip_banned"},
	}})
	policy, err := h.svc.DetectionPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.svc.readVerdicts(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	if got["r1"] != "clean" {
		t.Fatalf("r1 = %q, want clean: an unregistered region outvoted the registered point", got["r1"])
	}
}

// В1 / owner's decision 27.10: an observation point on a machine of the fleet
// is forbidden — a seized box would then judge the fleet and read the pool.
func TestAFleetBoxWithRelaysCannotBeAVantage(t *testing.T) {
	ph := newProvHarness(t)
	ctx := context.Background()
	box, _ := ph.harness.box()
	ph.relay(box, KindPersonal)

	slot := "fleet-" + uuid.NewString()[:6]
	t.Cleanup(func() {
		_, _ = ph.pool.Exec(context.Background(), `DELETE FROM entry_vantages WHERE slot = $1`, slot)
	})
	if _, err := ph.svc.CreateVantage(ctx, VantageSpec{Slot: slot, Box: box}, "test"); !errors.Is(err, ErrRejected) {
		t.Fatalf("CreateVantage on a box with relays: err = %v, want ErrRejected", err)
	}
	if _, err := ph.svc.SaveVantage(ctx, VantageSpec{Slot: slot, Box: box}, "test"); !errors.Is(err, ErrRejected) {
		t.Fatalf("SaveVantage on a box with relays: err = %v, want ErrRejected", err)
	}
	if _, err := ph.prov.DeployProber(ctx, box, slot, ""); !errors.Is(err, ErrRejected) {
		t.Fatalf("DeployProber on a box with relays: err = %v, want ErrRejected", err)
	}
}

// В2 (б): a vantage nobody registered claims nothing.
func TestAnUnregisteredRegionCannotClaimATask(t *testing.T) {
	h := newHarness(t)
	box, _ := h.box()
	r := h.relay(box, KindPersonal)
	id := h.oldestTask(TaskAdmission, []string{r}, "")
	task, err := h.svc.ClaimProbeTask(context.Background(), "zz", TaskAdmission)
	if err == nil && task != nil {
		t.Fatalf("unregistered region zz claimed task %s", task.ID)
	}
	if s := h.taskStatus(id); s != "queued" {
		t.Fatalf("task is %s after a refused claim, want queued", s)
	}
}

// В2 (в): a finish answers the task it was given — its relays, in the
// prober's own words — or it is not an answer.
func TestAFinishMustAnswerTheTaskItWasGiven(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	slot := h.openSlot("a")
	box, _ := h.box()
	r := h.relay(box, KindPersonal)

	for name, verdicts := range map[string]map[string]string{
		"a relay the task never named": {r: "clean", "someone-else-1": "ip_banned"},
		"a word the prober never says": {r: "x"},
		"no word about its relay":      {"x": "clean"},
	} {
		id := h.oldestTask(TaskAdmission, []string{r}, "")
		task, err := h.svc.ClaimProbeTask(ctx, slot, TaskAdmission)
		if err != nil || task == nil || task.ID != id {
			t.Fatalf("%s: claim = %v, %v; want task %s", name, task, err, id)
		}
		if err := h.svc.FinishProbeTask(ctx, id, slot, verdicts, nil); !errors.Is(err, ErrRejected) {
			t.Errorf("%s: finish err = %v, want ErrRejected", name, err)
		}
		_, _ = h.pool.Exec(ctx, `DELETE FROM entry_probe_tasks WHERE id = $1`, id)
	}
}

// The mine under В2: a claim that cannot hand the relays over must leave the
// task where it was, not claimed by a vantage that never got it.
func TestAClaimThatCannotOpenItsRelaysLeavesTheTaskQueued(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	slot := h.openSlot("a")
	box, _ := h.box()
	r := h.relay(box, KindPersonal)
	if _, err := h.pool.Exec(ctx, `UPDATE entry_relays SET identity_ct = '\x00'::bytea WHERE name = $1`, r); err != nil {
		t.Fatal(err)
	}
	id := h.oldestTask(TaskAdmission, []string{r}, "")
	if _, err := h.svc.ClaimProbeTask(ctx, slot, TaskAdmission); err == nil {
		t.Fatal("claim over an unreadable relay succeeded")
	}
	if s := h.taskStatus(id); s != "queued" {
		t.Fatalf("task is %s after a failed claim, want queued", s)
	}
}

// С7: the prober's own engine failing says nothing about the machine. An
// admission that read probe_failed must not refuse it (and hand a paid
// machine back) — it is asked again, and at the deadline it is "could not
// judge", never "не допущен".
func TestAnAdmissionTheProberCouldNotRunIsNoRefusal(t *testing.T) {
	ph := newProvHarness(t)
	ph.prov.probeTimeout = 600 * time.Millisecond
	ph.vantage("probe_failed")
	ph.vantage("probe_failed")
	_, id := ph.newBox("provider-pw-123")
	j := ph.wait(id)
	if j.Status != "failed" {
		t.Fatalf("job = %+v, want failed (nothing judged)", j)
	}
	if strings.Contains(j.Error, "не допущен") {
		t.Fatalf("a prober that could not run refused the machine: %s", j.Error)
	}
}

// openSlotWithToken is openSlot, returning the slot's token too.
func (h *harness) openSlotWithToken(prefix string) (slot, token string) {
	h.t.Helper()
	slot = h.openSlot(prefix)
	token, err := h.svc.IssueVantageToken(context.Background(), slot)
	if err != nil {
		h.t.Fatal(err)
	}
	return slot, token
}

func proberCall(t *testing.T, srv *httptest.Server, method, path, token, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+"/api/v1/internal/entry-probes"+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

// В2 (а), on the wire: the slot is the token's. A claim names no region, a
// task claimed by A is finished by A only, and a token no active slot holds
// opens nothing — not the pool, not a task.
func TestTheProberRoutesSpeakForTheTokensSlotOnly(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	mux := http.NewServeMux()
	RegisterProberRoutes(mux, h.svc)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	a, tokA := h.openSlotWithToken("a")
	_, tokB := h.openSlotWithToken("b")
	gone, tokGone := h.openSlotWithToken("gone")
	if _, err := h.svc.RetireVantage(ctx, gone, "test"); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"", "nope", tokGone} {
		if code, _ := proberCall(t, srv, "GET", "/relays", tok, ""); code != http.StatusUnauthorized {
			t.Fatalf("relays with token %q: %d, want 401", tok, code)
		}
		if code, _ := proberCall(t, srv, "POST", "/tasks/claim", tok, `{"kind":"admission"}`); code != http.StatusUnauthorized {
			t.Fatalf("claim with token %q: %d, want 401", tok, code)
		}
	}

	box, _ := h.box()
	r := h.relay(box, KindPersonal)
	id := h.oldestTask(TaskAdmission, []string{r}, "")
	// A region in the body is nobody's word any more: the claim is A's.
	code, body := proberCall(t, srv, "POST", "/tasks/claim", tokA, `{"kind":"admission","region":"zz"}`)
	if code != http.StatusOK || !strings.Contains(body, id.String()) {
		t.Fatalf("claim as A: %d %s", code, body)
	}
	var by string
	if err := h.pool.QueryRow(ctx, `SELECT claimed_by FROM entry_probe_tasks WHERE id = $1`, id).Scan(&by); err != nil || by != a {
		t.Fatalf("claimed_by = %q (%v), want %s", by, err, a)
	}
	answer := `{"verdicts":{"` + r + `":"ip_banned"}}`
	if code, _ := proberCall(t, srv, "POST", "/tasks/"+id.String(), tokB, answer); code != http.StatusConflict {
		t.Fatalf("B finishing A's task: %d, want 409", code)
	}
	if code, _ := proberCall(t, srv, "POST", "/tasks/"+id.String(), tokA, `{"verdicts":{"`+r+`":"x"}}`); code != http.StatusBadRequest {
		t.Fatalf("A answering with a word the prober never says: %d, want 400", code)
	}
	if s := h.taskStatus(id); s != "claimed" {
		t.Fatalf("task is %s after two refused finishes", s)
	}
	if code, _ := proberCall(t, srv, "POST", "/tasks/"+id.String(), tokA, `{"verdicts":{"`+r+`":"clean"}}`); code != http.StatusNoContent {
		t.Fatalf("A finishing its own task: %d, want 204", code)
	}
}

// The class (generative): whatever a finish carries — subjects the task
// named or not, words the prober says or not, the claimer or another slot —
// it is recorded only when it is the claimer's complete answer in the kind's
// words, and refused otherwise with nothing written.
func TestAFinishIsRecordedOnlyAsTheClaimersAnswer(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := h.openSlot("a")
	b := h.openSlot("b")
	box, _ := h.box()
	r1, r2 := h.relay(box, KindPersonal), h.relay(box, KindPersonal)
	names := []string{r1, r2, "other-1", ""}
	words := []string{"clean", "ip_banned", "probe_failed", "benched", "x", ""}
	rng := mrand.New(mrand.NewPCG(1, 2))
	recorded := 0
	for i := 0; i < 120; i++ {
		id := h.oldestTask(TaskAdmission, []string{r1, r2}, "")
		if task, err := h.svc.ClaimProbeTask(ctx, a, TaskAdmission); err != nil || task == nil || task.ID != id {
			t.Fatalf("claim: %v %v", task, err)
		}
		v := map[string]string{}
		for n := rng.IntN(4); n > 0; n-- {
			v[names[rng.IntN(len(names))]] = words[rng.IntN(len(words))]
		}
		if rng.IntN(3) == 0 {
			v = map[string]string{r1: words[rng.IntN(3)], r2: words[rng.IntN(3)]}
		}
		by := a
		if rng.IntN(4) == 0 {
			by = b
		}
		err := h.svc.FinishProbeTask(ctx, id, by, v, nil)
		complete := len(v) == 2 && relayVerdictWords[v[r1]] && relayVerdictWords[v[r2]]
		want := by == a && complete
		if (err == nil) != want {
			t.Fatalf("finish by %s with %v: err = %v, want recorded = %v", by, v, err, want)
		}
		got, _ := h.svc.probeVerdicts(ctx, id)
		if want {
			recorded++
			if got[r1] != v[r1] || got[r2] != v[r2] || len(got) != 2 {
				t.Fatalf("recorded %v, sent %v", got, v)
			}
		} else if got != nil {
			t.Fatalf("a refused finish left verdicts %v", got)
		}
		_, _ = h.pool.Exec(ctx, `DELETE FROM entry_probe_tasks WHERE id = $1`, id)
	}
	if recorded == 0 {
		t.Fatal("no finish was ever recorded: the generator proves nothing")
	}
}

// The class on the read side (generative): however many regions nobody
// registered write whatever verdicts, the merged reading is the registered
// points' alone.
func TestUnregisteredRegionsNeverMoveTheReading(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a, b := h.openSlot("a"), h.openSlot("b")
	policy, err := h.svc.DetectionPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	words := []string{"clean", "ip_banned", "fingerprint", "network_down", "unknown", "domain_blocked"}
	relays := []string{"r1", "r2", "r3"}
	rng := mrand.New(mrand.NewPCG(3, 4))
	for i := 0; i < 200; i++ {
		known := map[string]RelayVerdicts{a: {}, b: {}}
		for _, s := range []string{a, b} {
			for _, r := range relays {
				known[s][r] = words[rng.IntN(len(words))]
			}
		}
		all := map[string]RelayVerdicts{a: known[a], b: known[b]}
		for n := rng.IntN(5); n > 0; n-- {
			rv := RelayVerdicts{}
			for _, r := range relays {
				rv[r] = words[rng.IntN(len(words))]
			}
			all[[]string{"zz", "ru-msk", "a-x", testVantage + "-fake", ""}[rng.IntN(5)]] = rv
		}
		h.svc.SetVerdicts(&fakeMultiVerdicts{byVantage: all})
		got, err := h.svc.readVerdicts(ctx, policy)
		if err != nil {
			t.Fatal(err)
		}
		want := MergeVantageVerdicts(known, policy)
		for _, r := range relays {
			if got[r] != want[r] {
				t.Fatalf("%s = %q, want %q: an unregistered region moved the reading (%v)", r, got[r], want[r], all)
			}
		}
	}
}
