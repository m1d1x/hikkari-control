package entry

import (
	"context"
	"fmt"
	"time"
)

// TrialPrimeSizingForTest substitutes synthetic demand while retaining the
// real fleet, budget, provisioner, admission and handback code paths.
func TrialPrimeSizingForTest(s *Service, pool string, target int) {
	sz := sizingFor(max(1, target))
	if target == 0 {
		sz.Demand.ForecastMbps = 0
	}
	s.primeSizing(map[string]PoolSizing{pool: sz})
}

// TrialConsiderForTest runs the same automatic decision as Mechanic.Pass,
// scoped to the trial's own incident instead of unrelated production repairs.
func TrialConsiderForTest(m *Mechanic, ctx context.Context, inc Incident) (bool, bool) {
	return m.consider(ctx, inc)
}

// TrialHandBackForTest advances the ordinary surplus pass for trial-owned boxes only.
func TrialHandBackForTest(p *Provisioner, ctx context.Context, boxes []string) ([]string, error) {
	if len(boxes) == 0 {
		return nil, fmt.Errorf("trial handback requires owned boxes")
	}
	return p.handBackSurplus(ctx, boxes...)
}

// TrialPrimeHandbackForTest models demand that keeps the baseline pool intact.
func TrialPrimeHandbackForTest(s *Service, pool string, keep int) {
	sz := sizingFor(0)
	sz.Demand.ForecastMbps = float64(max(1, keep)) * sz.Capacity.Mbps * fleetShrinkHeadroom
	s.primeSizing(map[string]PoolSizing{pool: sz})
}

// TrialClockForTest accelerates the paid-hour window after real admission.
func TrialClockForTest(s *Service, now func() time.Time) { s.now = now }

// TrialStopForTest joins in-flight jobs before cleanup can delete resources.
func TrialStopForTest(s *Service) { s.stopBackground() }

// TrialReleaseForTest reconciles only orders and boxes attempted by this trial.
func TrialReleaseForTest(s *Service, ctx context.Context, boxes []string) (int, error) {
	if len(boxes) == 0 {
		return 0, nil
	}
	return s.releaseUndeleted(ctx, boxes)
}
