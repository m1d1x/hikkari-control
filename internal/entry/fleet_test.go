package entry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/m1d1x/hikkari-control/internal/auth"
)

// fleetHarness is a hosting harness with a voice pool of its own: hysteria2 in
// front of a region nobody else uses. The test database keeps every box every
// run made, so a pool of a common protocol would count other tests' machines;
// a fresh region is a pool that starts empty.
type fleetHarness struct {
	*hostHarness
	region string
	key    string
}

func newFleetHarness(t *testing.T) *fleetHarness {
	t.Helper()
	h := newHostHarness(t)
	h.prov.media = fixedMedia{{IP: "203.0.113.9", From: 40000, To: 40099}}
	region := "f-" + uuid.NewString()[:8]
	fh := &fleetHarness{hostHarness: h, region: region, key: FleetPool(ProtoHysteria2, region)}
	t.Cleanup(func() {
		ctx := context.Background()
		// The pool's incidents take their repairs with them (cascade), so the
		// fleet-wide rails of the next test do not read this one's attempts.
		if _, err := h.pool.Exec(ctx, `DELETE FROM entry_incidents WHERE pool = $1`, fh.key); err != nil {
			t.Errorf("clean up pool incidents: %v", err)
		}
		if _, err := h.pool.Exec(ctx, `DELETE FROM entry_fleet_pools WHERE voice_region = $1`, region); err != nil {
			t.Errorf("clean up pool: %v", err)
		}
		for _, b := range fh.boxes() {
			h.retire(b)
		}
	})
	return fh
}

// boxes is every machine of this pool still in service.
func (h *fleetHarness) boxes() []string {
	h.t.Helper()
	var out []string
	if err := h.pool.QueryRow(context.Background(), `
		SELECT coalesce(array_agg(name ORDER BY name), '{}') FROM entry_boxes
		WHERE proto = 'hysteria2' AND region = $1 AND retired_at IS NULL`, h.region).Scan(&out); err != nil {
		h.t.Fatalf("pool boxes: %v", err)
	}
	return out
}

// target makes the pool's load ask for n machines, as the dataset would read
// a pool carrying just under n machines' worth (sizingFor), and makes the
// pool one the hub keeps even before its first machine.
func (h *fleetHarness) target(n int) {
	h.t.Helper()
	if _, err := h.pool.Exec(context.Background(), `
		INSERT INTO entry_fleet_pools (proto, voice_region) VALUES ('hysteria2', $1)
		ON CONFLICT DO NOTHING`, h.region); err != nil {
		h.t.Fatalf("keep pool: %v", err)
	}
	h.svc.primeSizing(map[string]PoolSizing{h.key: sizingFor(n)})
}

// sizingFor is a fresh, complete reading of a pool whose load asks for
// exactly n machines of a 100 Mbit/s plan.
func sizingFor(n int) PoolSizing {
	return PoolSizing{
		Demand: &PoolDemand{
			Basis: DemandFlat, ForecastMbps: float64(n)*100*fleetHeadroom - 1,
			Fresh: true, Complete: true,
		},
		Capacity: &PlanCapacity{Provider: "test", Plan: "test", Mbps: 100, Source: CapacityCPU},
	}
}

// mode sets the owner's mode for short_of_fleet, and puts the default back.
func (h *fleetHarness) mode(mode string) {
	h.t.Helper()
	ctx := context.Background()
	if err := h.svc.SetPlaybookMode(ctx, CauseShortOfFleet, mode, "test"); err != nil {
		h.t.Fatalf("set mode: %v", err)
	}
	h.t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `DELETE FROM entry_playbook_modes WHERE cause = $1`, CauseShortOfFleet)
	})
}

// shortage runs the fleet pass and returns the pool's standing incident.
func (h *fleetHarness) shortage() (Incident, bool) {
	h.t.Helper()
	ctx := context.Background()
	if _, _, err := h.svc.DetectFleet(ctx); err != nil {
		h.t.Fatalf("detect fleet: %v", err)
	}
	incs, err := h.svc.Incidents(ctx, 1)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, inc := range incs {
		if inc.Pool == h.key && (inc.Status == IncidentOpen || inc.Status == IncidentActing) {
			return inc, true
		}
	}
	return Incident{}, false
}

// settle waits until the incident's repair has finished — the repair runs on
// its own goroutine — and returns the incident as it then stands.
func (h *fleetHarness) settle(id uuid.UUID) Incident {
	h.t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(20 * time.Second)
	for {
		inc, err := h.svc.Incident(ctx, id)
		if err != nil {
			h.t.Fatal(err)
		}
		if inc.Status != IncidentActing {
			return inc
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("the repair of %s never finished", id)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// inPool orders a machine into the pool by hand, as the console does, and
// waits for it to carry people.
func (h *fleetHarness) inPool() string {
	h.t.Helper()
	h.vantage("clean")
	box, job, err := h.prov.OrderBox(context.Background(), OrderSpec{Proto: ProtoHysteria2, VoiceRegion: h.region}, "")
	if err != nil {
		h.t.Fatal(err)
	}
	h.made = append(h.made, box+"-1")
	if j := h.wait(job); j.Status != "done" {
		h.t.Fatalf("provision %+v\n%s", j, h.logText(job))
	}
	return box
}

// bookPoolRepair writes a finished attempt on the pool, this long ago.
func (h *fleetHarness) bookPoolRepair(incident uuid.UUID, status string, ago time.Duration) {
	h.t.Helper()
	_, err := h.pool.Exec(context.Background(), `
		INSERT INTO entry_repairs (incident_id, pool, cause, playbook, decided_by, status,
		                           started_at, finished_at)
		VALUES ($1, $2, $3, $4, 'auto', $5, $6, $6)
	`, incident, h.key, CauseShortOfFleet, PlaybookScaleUp, status, time.Now().Add(-ago))
	if err != nil {
		h.t.Fatalf("book pool repair: %v", err)
	}
}

// A pool short of its target opens one incident, and the hub — by itself,
// `auto` being the default for a purchase since П9.19 — stands up every
// machine the pool is short of in one repair, not one a pass.
func TestAShortPoolGetsEveryMachineItNeedsAtOnce(t *testing.T) {
	h := newFleetHarness(t)
	ctx := context.Background()
	if p := PlaybookForCause(CauseShortOfFleet); p.Mode != ModeAuto || !p.Spends {
		t.Fatalf("playbook for short_of_fleet = %+v, want auto, spending", p)
	}
	h.target(2)
	orders := h.host.orderCount()

	inc, ok := h.shortage()
	if !ok {
		t.Fatal("a pool of 0 machines with a target of 2 opened no incident")
	}
	if inc.Box != "" || inc.Evidence["live"] != "0" || inc.Evidence["target"] != "2" {
		t.Errorf("incident = %+v", inc)
	}
	// The pass is idempotent: the same pool, the same incident.
	if again, _ := h.shortage(); again.ID != inc.ID {
		t.Fatalf("a second pass opened %s next to %s", again.ID, inc.ID)
	}

	// One admission answer per machine.
	h.vantage("clean")
	h.vantage("clean")
	m := NewMechanic(h.svc, h.prov)
	if started, _ := m.consider(ctx, inc); !started {
		t.Fatal("the hub did not add a machine on auto")
	}
	// While it runs, one order at a time: the same pass a minute later starts
	// nothing.
	if started, _ := m.consider(ctx, inc); started {
		t.Fatal("a second scale_up started while the first was running")
	}
	done := h.settle(inc.ID)
	if done.Status != IncidentResolved || done.ReplacedBy == "" || done.CostCents != 1000 {
		t.Fatalf("after the repair: %+v", done)
	}
	if n := h.host.orderCount(); n != orders+2 {
		t.Fatalf("orders %d → %d, want both machines at once", orders, n)
	}
	for _, b := range h.boxes() {
		if h.aliveOn(b) == 0 {
			t.Errorf("the new machine %s carries nobody", b)
		}
	}
	if _, ok := h.shortage(); ok {
		t.Error("a pool at its target still reads short")
	}
}

// The day's money is what stops a scale-up, not a count of orders: with room
// for one machine of the two the pool wants, one is bought, and the shortage
// left is one of money — short_of_budget, with no repair — until a cap moves.
func TestTheDayCapStopsAScaleUpHalfway(t *testing.T) {
	h := newFleetHarness(t)
	ctx := context.Background()
	// The dear plan, ordered and priced: a room of one and a half machines
	// is unambiguous at any hour of the day.
	if _, err := h.svc.SaveProvider(ctx, fakeHostKind, ProviderUpdate{
		Enabled: true, DefaultRegion: "r1", DefaultPlan: "huge",
	}, ""); err != nil {
		t.Fatal(err)
	}
	h.pricedTarget(2)
	h.spendRoom(SpendHosting, 1.5*machineDayOf(hugeCents))
	orders := h.host.orderCount()
	inc, ok := h.shortage()
	if !ok {
		t.Fatal("no shortage opened")
	}
	h.vantage("clean")
	if started, _ := NewMechanic(h.svc, h.prov).consider(ctx, inc); !started {
		t.Fatal("scale_up did not start")
	}
	done := h.settle(inc.ID)
	if n := h.host.orderCount(); n != orders+1 {
		t.Fatalf("orders %d → %d, want the one the day had room for", orders, n)
	}
	if !strings.Contains(done.Outcome, "1 из 2") {
		t.Errorf("outcome = %q", done.Outcome)
	}
	if _, _, err := h.svc.DetectFleet(ctx); err != nil {
		t.Fatal(err)
	}
	standing, err := h.svc.Incidents(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	budget := false
	for _, x := range standing {
		if x.Pool == h.key && x.Status == IncidentOpen {
			if x.Cause == CauseShortOfFleet {
				t.Errorf("the pool the day stops still reads short_of_fleet: %+v", x)
			}
			budget = budget || x.Cause == CauseShortOfBudget
		}
	}
	if !budget {
		t.Fatal("the shortage left past the day's cap is not short_of_budget")
	}
}

func ptr[T any](v T) *T { return &v }

// A pool at its target is left alone, and an incident standing on a pool
// whose target was lowered under it is closed by the next pass.
func TestAPoolAtItsTargetOrdersNothing(t *testing.T) {
	h := newFleetHarness(t)
	h.inPool()
	orders := h.host.orderCount()

	h.target(1)
	if inc, ok := h.shortage(); ok {
		t.Fatalf("a pool at 1 of 1 opened %+v", inc)
	}
	h.target(2)
	inc, ok := h.shortage()
	if !ok {
		t.Fatal("a pool at 1 of 2 opened nothing")
	}
	h.target(1)
	if _, ok := h.shortage(); ok {
		t.Fatal("the shortage stood after the target came down to what the pool holds")
	}
	closed, err := h.svc.Incident(context.Background(), inc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != IncidentResolved || !strings.Contains(closed.Outcome, "добран") {
		t.Errorf("closed as %q: %q", closed.Status, closed.Outcome)
	}
	if n := h.host.orderCount(); n != orders {
		t.Errorf("orders %d → %d for a pool at its target", orders, n)
	}
}

// `ask` is still the owner's to set for a purchase: the hub opens the
// shortage, proposes scale_up, and waits for the button.
func TestAShortPoolWaitsForAPersonOnAsk(t *testing.T) {
	h := newFleetHarness(t)
	ctx := context.Background()
	h.mode(ModeAsk)
	h.target(1)
	orders := h.host.orderCount()
	inc, ok := h.shortage()
	if !ok {
		t.Fatal("no shortage opened")
	}
	if started, handed := NewMechanic(h.svc, h.prov).consider(ctx, inc); started || handed {
		t.Fatalf("the hub acted on ask by itself: started=%v handed=%v", started, handed)
	}
	if n := h.host.orderCount(); n != orders {
		t.Fatalf("%d machines bought without a person", n-orders)
	}

	// The button is how a person says yes.
	h.vantage("clean")
	if err := NewMechanic(h.svc, h.prov).Start(ctx, inc.ID, "admin-1", ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	if got := h.settle(inc.ID); got.Status != IncidentResolved || got.DecidedBy != DecidedAsk {
		t.Fatalf("after the button: %+v", got)
	}
}

// One acquisition at a time across the fleet: another pool's scale-up running
// holds this one, without giving up on it.
func TestAScaleUpWaitsForAnotherAcquisitionInFlight(t *testing.T) {
	h := newFleetHarness(t)
	ctx := context.Background()
	h.mode(ModeAuto)
	h.target(1)

	other, _ := h.harness.box()
	banned := h.openIncident(other, CauseIPBanned, map[string]string{other + "-1": "ip_banned"})
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO entry_repairs (incident_id, pool, cause, playbook, decided_by)
		VALUES ($1, $2, $3, $4, 'ask')
	`, banned.ID, ProtoVlessXhttp, CauseShortOfFleet, PlaybookScaleUp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(),
			`UPDATE entry_repairs SET status = 'failed', finished_at = now() WHERE incident_id = $1 AND status = 'running'`, banned.ID)
	})
	orders := h.host.orderCount()
	inc, ok := h.shortage()
	if !ok {
		t.Fatal("no shortage opened")
	}
	if started, handed := NewMechanic(h.svc, h.prov).consider(ctx, inc); started || handed {
		t.Fatalf("scale_up beside another acquisition: started=%v handed=%v", started, handed)
	}
	if n := h.host.orderCount(); n != orders {
		t.Fatalf("orders %d → %d", orders, n)
	}
}

// The shelf comes before the hoster for a scale-up too: nothing is excepted,
// and an idle machine of the pool is taken instead of buying one.
func TestAScaleUpTakesAnIdleMachineOfThePool(t *testing.T) {
	h := newFleetHarness(t)
	ctx := context.Background()
	idle, err := h.prov.orderAndRecord(ctx, OrderSpec{Proto: ProtoHysteria2, VoiceRegion: h.region})
	if err != nil {
		t.Fatal(err)
	}
	h.made = append(h.made, idle+"-1")
	_ = h.prov.execJob(ctx, JobProvision, idle, uuid.Nil, func(*jobRun) error {
		return errors.New("разворот упал до релеев")
	})
	h.target(1)
	orders := h.host.orderCount()
	inc, ok := h.shortage()
	if !ok {
		t.Fatal("an idle machine carrying nobody was counted as live")
	}

	h.vantage("clean")
	res, err := NewMechanic(h.svc, h.prov).perform(ctx, inc, PlaybookScaleUp)
	if err != nil {
		t.Fatalf("scale_up: %v", err)
	}
	if n := h.host.orderCount(); n != orders {
		t.Errorf("orders %d → %d with an idle machine on the shelf", orders, n)
	}
	if res.replacedBy != idle || h.aliveOn(idle) == 0 {
		t.Errorf("result %+v: want the idle %s in the pool", res, idle)
	}
	if !strings.Contains(res.outcome, "взята простаивающая "+idle) {
		t.Errorf("outcome = %q", res.outcome)
	}
}

// A scale-up pressed after the pool filled up by other means buys nothing:
// the shortage is read again inside the repair.
func TestAScaleUpForAPoolThatFilledMeanwhileBuysNothing(t *testing.T) {
	h := newFleetHarness(t)
	ctx := context.Background()
	h.target(1)
	inc, ok := h.shortage()
	if !ok {
		t.Fatal("no shortage opened")
	}
	h.inPool()
	orders := h.host.orderCount()
	res, err := NewMechanic(h.svc, h.prov).perform(ctx, inc, PlaybookScaleUp)
	if err != nil {
		t.Fatalf("scale_up: %v", err)
	}
	if n := h.host.orderCount(); n != orders || res.cost != 0 || !strings.Contains(res.outcome, "уже добран") {
		t.Errorf("orders %d → %d, result %+v", orders, n, res)
	}
}

// A machine born banned is handed back, and the attempt is a strike: after
// FailStrikes of them the shortage goes to a person instead of a fourth
// purchase.
func TestAMachineBornBannedIsAStrike(t *testing.T) {
	h := newFleetHarness(t)
	ctx := context.Background()
	h.mode(ModeAuto)
	h.target(1)
	inc, ok := h.shortage()
	if !ok {
		t.Fatal("no shortage opened")
	}

	h.vantage("ip_banned")
	m := NewMechanic(h.svc, h.prov)
	if started, _ := m.consider(ctx, inc); !started {
		t.Fatal("scale_up did not start")
	}
	after := h.settle(inc.ID)
	if after.Status != IncidentOpen {
		t.Fatalf("a failed scale_up left the incident %q", after.Status)
	}
	repairs, err := h.svc.Repairs(ctx, inc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(repairs) != 1 || repairs[0].Status != repairFailed || repairs[0].Pool != h.key {
		t.Fatalf("repairs = %+v, want one failed on the pool", repairs)
	}
	if len(h.host.deletedIDs()) != 1 {
		t.Errorf("deleted = %v, want the banned newborn handed back", h.host.deletedIDs())
	}
	if len(h.boxes()) != 0 {
		t.Errorf("the pool holds %v after a machine born banned", h.boxes())
	}

	// Two more like it, past the cooldown, and the hub stops buying.
	pol, err := h.svc.DetectionPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < pol.FailStrikes; i++ {
		h.bookPoolRepair(inc.ID, repairFailed, pol.RepairCooldown+time.Minute)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE entry_repairs SET started_at = $2, finished_at = $2
		WHERE incident_id = $1 AND started_at > $3`, inc.ID, time.Now().Add(-pol.RepairCooldown-time.Minute),
		time.Now().Add(-pol.RepairCooldown)); err != nil {
		t.Fatal(err)
	}
	orders := h.host.orderCount()
	started, handed := m.consider(ctx, inc)
	if started || !handed {
		t.Fatalf("after %d strikes: started=%v handed=%v", pol.FailStrikes, started, handed)
	}
	got, _ := h.svc.Incident(ctx, inc.ID)
	if !strings.Contains(got.Handoff, "добор пула") {
		t.Errorf("handoff = %q", got.Handoff)
	}
	if n := h.host.orderCount(); n != orders {
		t.Errorf("orders %d → %d after the strikes ran out", orders, n)
	}
}

// The forecast: a pool at its target, whose machines die fast enough that a
// loss is likely before a new one could pass admission, reads short and is
// told why. With no loss history the same pool reads fine.
func TestTheForecastOrdersAheadOfTheLosses(t *testing.T) {
	h := newFleetHarness(t)
	ctx := context.Background()
	h.inPool()
	h.target(1)
	if inc, ok := h.shortage(); ok {
		t.Fatalf("a pool at target with no history opened %+v", inc)
	}

	// Three machines of this pool, ordered by the hub three hours ago,
	// admitted an hour after the order, banned now: λ = 3 losses over 6 hours
	// of carrying, the measured order-to-admission is an hour, the horizon
	// twice that, and Poisson(λ·1·2h ≈ 1) more likely than not loses one.
	now := time.Now()
	for i := 0; i < fleetMinLosses; i++ {
		name, _ := h.harness.box()
		if _, err := h.pool.Exec(ctx, `
			UPDATE entry_boxes SET provider = $2, proto = 'hysteria2', region = $3,
			       provider_instance_id = $4, created_at = $5
			WHERE name = $1`, name, fakeHostKind, h.region, fmt.Sprintf("hist-%s-%d", h.region, i),
			now.Add(-3*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := h.pool.Exec(ctx, `
			INSERT INTO entry_jobs (box, kind, status, created_at, finished_at)
			VALUES ($1, 'provision', 'done', $2, $3)`, name, now.Add(-3*time.Hour), now.Add(-2*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := h.svc.Retire(ctx, name, DeathIPBanned); err != nil {
			t.Fatal(err)
		}
	}

	forecasts, err := h.svc.FleetForecasts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var f FleetForecast
	for _, x := range forecasts {
		if x.Pool == h.key {
			f = x
		}
	}
	if f.Losses != fleetMinLosses || f.Buffer != 1 || f.Need != 1 {
		t.Fatalf("forecast = %+v", f)
	}
	if f.Horizon < 110*time.Minute || f.Horizon > 130*time.Minute {
		t.Errorf("horizon = %s, want twice the hour from order to admission", f.Horizon)
	}
	inc, ok := h.shortage()
	if !ok {
		t.Fatal("the forecast did not open a shortage")
	}
	if !strings.Contains(inc.Advice, "заранее") || inc.Evidence["buffer"] != "1" {
		t.Errorf("incident = %+v", inc)
	}
}

func TestPoissonMedian(t *testing.T) {
	for _, c := range []struct {
		mu   float64
		want int
	}{{0, 0}, {0.5, 0}, {0.68, 0}, {0.7, 1}, {1, 1}, {1.6, 1}, {1.7, 2}, {2, 2}, {10, 10}} {
		if got := poissonMedian(c.mu); got != c.want {
			t.Errorf("poissonMedian(%v) = %d, want %d", c.mu, got, c.want)
		}
	}
}

// The autopilot screen's surface: reading the modes and the fleet needs no
// touch, moving a mode does — it is a purchase the hub makes later. The
// fleet has no target to move any more (П9.16): the screen reads what the hub
// derived and from what.
func TestAdminAutopilotSurface(t *testing.T) {
	h := newFleetHarness(t)
	ctx := context.Background()
	var adminID string
	if err := h.pool.QueryRow(ctx, `
		INSERT INTO admins (login, display_name, password_hash, role)
		VALUES ($1, 'autopilot test', 'x', 'owner') RETURNING id::text`,
		"auto-"+uuid.NewString()[:8]).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = h.pool.Exec(context.Background(), `DELETE FROM entry_playbook_modes WHERE cause = $1`, CauseShortOfFleet)
	})
	steppedUp := false
	gate := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			next(w, r.WithContext(auth.WithAdmin(r.Context(), &auth.AdminInfo{ID: adminID})))
		}
	}
	stepUp := func(next http.HandlerFunc) http.HandlerFunc {
		return gate(func(w http.ResponseWriter, r *http.Request) {
			if !steppedUp {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "step_up_required"})
				return
			}
			next(w, r)
		})
	}
	mux := http.NewServeMux()
	RegisterAdminRoutes(mux, NewAdminHandlers(h.svc, h.prov, h.pool), gate, stepUp)
	do := func(method, path string, body any) (int, []byte) {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, &buf))
		return rec.Code, rec.Body.Bytes()
	}

	code, body := do("GET", "/api/v1/admin/entry/playbook-modes", nil)
	var modes struct {
		Modes []playbookModeRow `json:"modes"`
	}
	if code != 200 || json.Unmarshal(body, &modes) != nil {
		t.Fatalf("modes: %d %s", code, body)
	}
	var fleetRow playbookModeRow
	for _, m := range modes.Modes {
		if m.Cause == CauseShortOfFleet {
			fleetRow = m
		}
	}
	if fleetRow.Playbook != PlaybookScaleUp || fleetRow.Mode != ModeAuto || !fleetRow.Spends {
		t.Fatalf("short_of_fleet row = %+v", fleetRow)
	}

	path := "/api/v1/admin/entry/playbook-modes/" + CauseShortOfFleet
	if code, _ := do("PUT", path, map[string]string{"mode": ModeOff}); code != http.StatusForbidden {
		t.Fatalf("mode without a touch: %d", code)
	}
	steppedUp = true
	if code, body := do("PUT", path, map[string]string{"mode": ModeOff}); code != 200 ||
		!bytes.Contains(body, []byte(`"cause":"short_of_fleet","playbook":"scale_up","mode":"off","defaultMode":"auto"`)) {
		t.Fatalf("mode: %d %s", code, body)
	}
	if code, _ := do("PUT", path, map[string]string{"mode": "sometimes"}); code != http.StatusBadRequest {
		t.Fatalf("a mode that is not one: %d", code)
	}

	target := map[string]any{"proto": ProtoHysteria2, "voiceRegion": h.region, "targetBoxes": 2}
	if code, _ := do("PUT", "/api/v1/admin/entry/fleet/target", target); code == http.StatusOK {
		t.Fatalf("the owner's target is gone, yet a save answered %d", code)
	}
	h.target(2)
	code, body = do("GET", "/api/v1/admin/entry/fleet", nil)
	var fleet struct {
		Pools      []fleetPoolRow `json:"pools"`
		Capacities []PlanCapacity `json:"capacities"`
	}
	if code != 200 || json.Unmarshal(body, &fleet) != nil || fleet.Capacities == nil {
		t.Fatalf("fleet: %d %s", code, body)
	}
	found := false
	for _, p := range fleet.Pools {
		if p.Pool == h.key {
			found = p.Target == 2 && p.Need == 2 && p.Proto == ProtoHysteria2 && p.VoiceRegion == h.region &&
				strings.Contains(p.TargetWhy, "нужно 2") && p.Sizing.Capacity != nil && p.Sizing.Capacity.Mbps == 100
		}
	}
	if !found {
		t.Errorf("fleet as derived: %s", body)
	}

	// The fleet's knobs in units are gone (П9.19): money is the one knob,
	// and it lives on its own endpoint.
	code, body = do("GET", "/api/v1/admin/entry/policy", nil)
	if code != 200 || bytes.Contains(body, []byte(`"fleetOrdersPerHour"`)) || bytes.Contains(body, []byte(`"domainPoolTarget"`)) {
		t.Errorf("policy still carries the fleet knobs: %d %s", code, body)
	}
	code, body = do("GET", "/api/v1/admin/entry/spend", nil)
	if code != 200 || !bytes.Contains(body, []byte(`"role":"hosting"`)) || !bytes.Contains(body, []byte(`"role":"registrar"`)) {
		t.Errorf("spend: %d %s", code, body)
	}
	steppedUp = false
	if code, _ := do("PUT", "/api/v1/admin/entry/spend/hosting", map[string]int{"dailyCapCents": 100, "monthlyCapCents": 1000}); code != http.StatusForbidden {
		t.Errorf("caps without a touch: %d, want 403", code)
	}
}

// resolvedShortage records a shortage of this pool that a scale_up cured: an
// incident closed as resolved, with the done repair that closed it, this long
// ago.
func (h *fleetHarness) resolvedShortage(ago time.Duration) {
	h.t.Helper()
	var id uuid.UUID
	at := time.Now().Add(-ago)
	if err := h.pool.QueryRow(context.Background(), `
		INSERT INTO entry_incidents (pool, cause, advice, evidence, opened_at, status, closed_at)
		VALUES ($1, $2, 'тест', '{}', $3, 'resolved', $3)
		RETURNING id`, h.key, CauseShortOfFleet, at).Scan(&id); err != nil {
		h.t.Fatalf("resolved shortage: %v", err)
	}
	h.bookPoolRepair(id, repairDone, ago)
}

// A scale_up that bought a machine and closed its shortage is not a strike.
// Found on the stand 27.10: three purchases that each worked, on three bans
// that day, handed the fourth shortage to a person as "tried three times and
// it did not work" — and the pool sat one machine short. What bounds a run of
// purchases is the money gate, not the strikes; a strike is an attempt that
// did not take.
func TestSuccessfulScaleUpsAreNotStrikes(t *testing.T) {
	h := newFleetHarness(t)
	ctx := context.Background()
	h.mode(ModeAuto)
	h.target(1)
	pol, err := h.svc.DetectionPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < pol.FailStrikes; i++ {
		h.resolvedShortage(pol.RepairCooldown + time.Duration(i+1)*time.Minute)
	}
	inc, ok := h.shortage()
	if !ok {
		t.Fatal("no shortage opened")
	}

	h.vantage("clean")
	started, handed := NewMechanic(h.svc, h.prov).consider(ctx, inc)
	if handed {
		got, _ := h.svc.Incident(ctx, inc.ID)
		t.Fatalf("%d purchases that worked handed the next shortage to a person: %q", pol.FailStrikes, got.Handoff)
	}
	if !started {
		t.Fatal("scale_up did not start after purchases that worked")
	}
	h.settle(inc.ID)
}

// And a scale_up that ended done while its shortage still stands did not
// take: it stays a strike, so a pool the hub cannot fill reaches a person.
func TestADoneScaleUpOnAStandingShortageIsAStrike(t *testing.T) {
	h := newFleetHarness(t)
	ctx := context.Background()
	h.mode(ModeAuto)
	h.target(1)
	inc, ok := h.shortage()
	if !ok {
		t.Fatal("no shortage opened")
	}
	pol, err := h.svc.DetectionPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < pol.FailStrikes; i++ {
		h.bookPoolRepair(inc.ID, repairDone, pol.RepairCooldown+time.Duration(i+1)*time.Minute)
	}

	started, handed := NewMechanic(h.svc, h.prov).consider(ctx, inc)
	if started || !handed {
		t.Fatalf("after %d done attempts on a standing shortage: started=%v handed=%v", pol.FailStrikes, started, handed)
	}
}

// leaving says the machine is being emptied for a handback.
func (h *fleetHarness) leaving(box string) bool {
	h.t.Helper()
	var since *time.Time
	if err := h.pool.QueryRow(context.Background(),
		`SELECT handback_since FROM entry_boxes WHERE name = $1`, box).Scan(&since); err != nil {
		h.t.Fatal(err)
	}
	return since != nil
}

// paidMinutesAgo dates the machines' creation so that their paid hour is that
// many minutes old.
func (h *hostHarness) paidMinutesAgo(minutes int, boxes ...string) {
	h.t.Helper()
	if _, err := h.pool.Exec(context.Background(),
		`UPDATE entry_boxes SET created_at = now() - make_interval(mins => $2) WHERE name = ANY($1)`,
		boxes, minutes); err != nil {
		h.t.Fatal(err)
	}
}
