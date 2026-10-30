package entry

import (
	"context"
	"testing"
	"time"
)

func TestEmptyAdmissionPollMakesAnEmptyFleetReady(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	policy := mustPolicy(t, h.svc)
	if why, err := h.svc.noLiveVantage(ctx, policy); err != nil || why == "" {
		t.Fatalf("unseen worker: %q %v", why, err)
	}
	if _, err := h.svc.ClaimProbeTask(ctx, testVantage, TaskBench); err != nil {
		t.Fatal(err)
	}
	if why, err := h.svc.noLiveVantage(ctx, policy); err != nil || why == "" {
		t.Fatalf("bench poll pretended to be admission readiness: %q %v", why, err)
	}
	if _, err := h.svc.ClaimProbeTask(ctx, testVantage, TaskAdmission); err != nil {
		t.Fatal(err)
	}
	if why, err := h.svc.noLiveVantage(ctx, policy); err != nil || why != "" {
		t.Fatalf("empty admission poll did not bootstrap fleet: %q %v", why, err)
	}
	// Contact expiration and retirement both close the gate again.
	if _, err := h.pool.Exec(ctx, `UPDATE entry_vantages SET last_admission_poll_at = $2 WHERE slot = $1`, testVantage, time.Now().Add(-2*policy.VerdictWindow)); err != nil {
		t.Fatal(err)
	}
	if why, err := h.svc.noLiveVantage(ctx, policy); err != nil || why == "" {
		t.Fatalf("expired worker: %q %v", why, err)
	}
	if err := h.svc.noteAdmissionPoll(ctx, h.pool, testVantage); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx, `UPDATE entry_vantages SET status='retired',retired_at=now() WHERE slot=$1`, testVantage); err != nil {
		t.Fatal(err)
	}
	if why, err := h.svc.noLiveVantage(ctx, policy); err != nil || why == "" {
		t.Fatalf("retired worker: %q %v", why, err)
	}
}
