package entry

// benchpolicy.go holds the canary's thresholds and sample size (roadmap 42
// П6.14, owner's decision 25.10).
//
// # Why they left the code
//
// `benchMinThroughputShare` (0.8) and `benchMaxRTTGrowth` (1.3) were Go
// constants, and the number of before/after pairs a verdict rested on was a
// hard-coded one, with a second pair run only when the first failed on
// throughput alone. The stand showed what that costs: throughput over the
// RU → AMS path swings by a factor of four between runs of ONE unchanged
// binary (15.4 … 63.4 Mbit/s over seven runs), and the rollout of the build
// that opens the XUDP door was rejected at 0.774 against 0.80 — a verdict
// about the hour, not about the build.
//
// # Why the answer is more samples and not a looser threshold
//
// A build that halves an entry point's throughput is exactly what a canary
// exists to catch, so throughput keeps its vote. What was wrong was the sample
// size: one pair cannot tell a slower build from a slower minute. Averaging N
// pairs shrinks the spread of the mean by about √N, and a longer run averages
// over more of the path's own drift. Both are knobs now, and both belong to
// the owner — a canary that takes half an hour instead of ten minutes costs
// nothing anybody notices, and buys a verdict that means something.
//
// # Nothing here invents a default
//
// Like [DetectionPolicy]: a hub whose policy row is missing is a broken hub,
// not a hub with defaults. Migration 067 creates the row, a failed read is an
// error, and the caller declines to act rather than judging a build by numbers
// that contradict the screen.

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// BenchPolicy is what the canary judges by, as the rollout and the console
// both read it.
type BenchPolicy struct {
	// MinThroughputShare is the share of the old build's throughput the new
	// one must keep. Stored as hundredths, carried here as the fraction the
	// comparison multiplies by.
	MinThroughputShare float64 `json:"-"`
	// MaxRTTGrowth is how much the chat RTT p50 may grow, as a factor.
	MaxRTTGrowth float64 `json:"-"`
	// Pairs is how many before/after pairs one verdict rests on. The pairs are
	// averaged and the comparison sees the average, so this is the knob that
	// buys certainty with time.
	Pairs int `json:"pairs"`
	// RunDuration is how long one run lasts.
	RunDuration time.Duration `json:"-"`

	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy string    `json:"updatedBy"`
}

// The console reads and writes whole numbers: a knob showing "80" and a knob
// showing "0.8000000001" are not the same knob, and a stepper needs the
// former.
func (p BenchPolicy) MinThroughputPct() int { return int(p.MinThroughputShare*100 + 0.5) }
func (p BenchPolicy) MaxRTTGrowthPct() int  { return int(p.MaxRTTGrowth*100 + 0.5) }
func (p BenchPolicy) RunSeconds() int       { return int(p.RunDuration / time.Second) }

// Benching is how long one canary's measuring will take at this policy,
// counting both sides of every pair. The console shows it beside the knobs:
// `pairs` is the one knob whose cost a person should see before turning it.
func (p BenchPolicy) Benching() time.Duration {
	return time.Duration(p.Pairs) * 2 * p.RunDuration
}

// benchPolicyCache is the Service's few-second memory of the row, for the same
// reason [policyCache] exists: a canary reads the policy per job, and the row
// changes a few times a year.
type benchPolicyCache struct {
	mu   sync.Mutex
	at   time.Time
	have bool
	val  BenchPolicy
}

// BenchPolicy reads the policy, from the cache when it is fresh.
func (s *Service) BenchPolicy(ctx context.Context) (BenchPolicy, error) {
	s.benchPolicy.mu.Lock()
	if s.benchPolicy.have && s.now().Sub(s.benchPolicy.at) < policyCacheTTL {
		p := s.benchPolicy.val
		s.benchPolicy.mu.Unlock()
		return p, nil
	}
	s.benchPolicy.mu.Unlock()

	p, err := s.readBenchPolicy(ctx)
	if err != nil {
		return BenchPolicy{}, err
	}

	s.benchPolicy.mu.Lock()
	s.benchPolicy.val, s.benchPolicy.at, s.benchPolicy.have = p, s.now(), true
	s.benchPolicy.mu.Unlock()
	return p, nil
}

// readBenchPolicy goes to the row itself.
func (s *Service) readBenchPolicy(ctx context.Context) (BenchPolicy, error) {
	var (
		p                       BenchPolicy
		thrPct, rttPct, runSecs int
	)
	err := s.pool.QueryRow(ctx, `
		SELECT min_throughput_pct, max_rtt_growth_pct, pairs, run_seconds, updated_at, updated_by
		FROM entry_bench_policy WHERE id
	`).Scan(&thrPct, &rttPct, &p.Pairs, &runSecs, &p.UpdatedAt, &p.UpdatedBy)
	if err != nil {
		return BenchPolicy{}, fmt.Errorf("entry: bench policy: %w", err)
	}
	p.MinThroughputShare = float64(thrPct) / 100
	p.MaxRTTGrowth = float64(rttPct) / 100
	p.RunDuration = time.Duration(runSecs) * time.Second
	return p, nil
}

// BenchPolicyPatch is what the console sends. Every field is optional, like
// [DetectionPolicyPatch]: the screen saves the knob that moved, and a knob it
// cannot show must not reset one it can.
type BenchPolicyPatch struct {
	MinThroughputPct *int `json:"minThroughputPct"`
	MaxRTTGrowthPct  *int `json:"maxRttGrowthPct"`
	Pairs            *int `json:"pairs"`
	RunSeconds       *int `json:"runSeconds"`
}

// SaveBenchPolicy applies a patch and returns the policy as it now stands.
//
// The bounds are the migration's, and a value outside them comes back as the
// database's own refusal rather than being clamped: a console that silently
// stored 100 when a person typed 1000 would show a threshold nobody chose.
func (s *Service) SaveBenchPolicy(ctx context.Context, patch BenchPolicyPatch, actor string) (BenchPolicy, error) {
	_, err := s.pool.Exec(ctx, `
		UPDATE entry_bench_policy SET
			min_throughput_pct = coalesce($1, min_throughput_pct),
			max_rtt_growth_pct = coalesce($2, max_rtt_growth_pct),
			pairs              = coalesce($3, pairs),
			run_seconds        = coalesce($4, run_seconds),
			updated_at = now(), updated_by = $5
		WHERE id
	`, patch.MinThroughputPct, patch.MaxRTTGrowthPct, patch.Pairs, patch.RunSeconds, actor)
	if err != nil {
		return BenchPolicy{}, fmt.Errorf("%w: %v", ErrRejected, err)
	}
	// Drop the cache rather than patch it: the row is the truth, and a read
	// right after a save must show what the database accepted.
	s.benchPolicy.mu.Lock()
	s.benchPolicy.have = false
	s.benchPolicy.mu.Unlock()
	return s.BenchPolicy(ctx)
}
