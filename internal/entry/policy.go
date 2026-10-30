package entry

// policy.go holds the eviction thresholds the detection screen turns (roadmap
// 42 К3).
//
// # Why they left the code
//
// How many failures in a row evict a point, and how many bans of one cause
// across the fleet count as a wave, are not facts about the program. They are
// judgements the owner revises while looking at the ban chart, and while they
// were Go constants revising one meant a rebuild. They live in one row of
// `entry_detection_policy` now (migration 065), and the console's detection
// screen writes that row.
//
// # Why reads are cached, and only for seconds
//
// The detector asks for the policy on every pass, and so does the mechanic on
// every incident. Those passes run on a timer, so a read per pass would be a
// query per second forever for a row that changes a few times a year. The
// cache is short on purpose: a knob turned in the console must take effect
// without a restart, and a few seconds of staleness is invisible against a
// probe round of a minute.
//
// # Nothing here invents a default
//
// A hub whose policy row is missing is a broken hub, not a hub with default
// thresholds: the row is created by the migration. So a failed read is
// returned as an error and the caller declines to act, rather than falling
// back to numbers that would contradict the screen.

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// policyCacheTTL is how long a read of the policy row is believed. Short
// enough that turning a knob in the console takes effect while the operator is
// still looking at the screen.
const policyCacheTTL = 5 * time.Second

// DetectionPolicy is the whole of the eviction policy, as the detector, the
// mechanic and the console all read it.
type DetectionPolicy struct {
	// FailStrikes is how many attempts on one (box, cause) inside a day the
	// hub makes before handing the incident to a person (strikeWindow).
	FailStrikes int `json:"failStrikes"`
	// WaveLimit and WaveWindow are the fuse: this many machines broken by one
	// cause inside the window and the hub stops acting on that cause.
	WaveLimit  int           `json:"waveLimit"`
	WaveWindow time.Duration `json:"-"`
	// VerdictWindow is how far back a probe verdict is believed.
	VerdictWindow time.Duration `json:"-"`
	// RepairCooldown is the gap after an attempt on the same cause.
	RepairCooldown time.Duration `json:"-"`
	// RequireTwoVantages asks for a ban to be seen from two observation
	// points before it counts. False while the grid holds one RU vantage —
	// see SaveDetectionPolicy for why the hub refuses to set it.
	RequireTwoVantages bool `json:"requireTwoVantages"`
	// IgnoreNetworkDown is a stored invariant, not a knob: the vantage's own
	// outage says nothing about a machine.
	IgnoreNetworkDown bool `json:"ignoreNetworkDown"`

	// The second step of each consumable's life (П9.11, secondchance.go):
	// how a suspect is reprobed, returned and written off.
	Domain SecondChance `json:"-"`
	Box    SecondChance `json:"-"`
	Cover  SecondChance `json:"-"`

	UpdatedAt time.Time `json:"updatedAt"`
	UpdatedBy string    `json:"updatedBy"`
}

// WaveWindowSeconds and the rest are what the console reads and writes:
// durations are seconds on the wire, because a knob showing "3600" and a knob
// showing "1h0m0s" are not the same knob.
func (p DetectionPolicy) WaveWindowSeconds() int     { return int(p.WaveWindow / time.Second) }
func (p DetectionPolicy) VerdictWindowSeconds() int  { return int(p.VerdictWindow / time.Second) }
func (p DetectionPolicy) RepairCooldownSeconds() int { return int(p.RepairCooldown / time.Second) }

// policyCache is the Service's few-second memory of the row.
type policyCache struct {
	mu   sync.Mutex
	at   time.Time
	have bool
	val  DetectionPolicy
}

// DetectionPolicy reads the policy, from the cache when it is fresh.
func (s *Service) DetectionPolicy(ctx context.Context) (DetectionPolicy, error) {
	s.policy.mu.Lock()
	if s.policy.have && s.now().Sub(s.policy.at) < policyCacheTTL {
		p := s.policy.val
		s.policy.mu.Unlock()
		return p, nil
	}
	s.policy.mu.Unlock()

	p, err := s.readDetectionPolicy(ctx)
	if err != nil {
		return DetectionPolicy{}, err
	}

	s.policy.mu.Lock()
	s.policy.val, s.policy.at, s.policy.have = p, s.now(), true
	s.policy.mu.Unlock()
	return p, nil
}

// policyColumns is the row as every read and every save returns it, in the
// order scanPolicy takes it.
const policyColumns = `fail_strikes, wave_limit, wave_window_seconds, verdict_window_seconds,
	repair_cooldown_seconds, require_two_vantages, ignore_network_down,
	domain_probe_every_seconds, domain_return_clean, domain_dead_fails, domain_dead_after_seconds,
	box_probe_every_seconds, box_return_clean, box_dead_fails, box_dead_after_seconds,
	cover_probe_every_seconds, cover_return_clean, cover_dead_fails, cover_dead_after_seconds,
	updated_at, updated_by`

// scanPolicy reads one row of policyColumns.
func scanPolicy(row pgx.Row) (DetectionPolicy, error) {
	var (
		p                                   DetectionPolicy
		waveSecs, verdictSecs, cooldownSecs int
		chance                              [3]struct{ every, after int }
	)
	err := row.Scan(&p.FailStrikes, &p.WaveLimit, &waveSecs, &verdictSecs, &cooldownSecs,
		&p.RequireTwoVantages, &p.IgnoreNetworkDown,
		&chance[0].every, &p.Domain.ReturnClean, &p.Domain.DeadFails, &chance[0].after,
		&chance[1].every, &p.Box.ReturnClean, &p.Box.DeadFails, &chance[1].after,
		&chance[2].every, &p.Cover.ReturnClean, &p.Cover.DeadFails, &chance[2].after,
		&p.UpdatedAt, &p.UpdatedBy)
	if err != nil {
		return DetectionPolicy{}, err
	}
	p.setDurations(waveSecs, verdictSecs, cooldownSecs)
	for i, c := range []*SecondChance{&p.Domain, &p.Box, &p.Cover} {
		c.ProbeEvery = time.Duration(chance[i].every) * time.Second
		c.DeadAfter = time.Duration(chance[i].after) * time.Second
	}
	return p, nil
}

// readDetectionPolicy goes to the row itself.
func (s *Service) readDetectionPolicy(ctx context.Context) (DetectionPolicy, error) {
	p, err := scanPolicy(s.pool.QueryRow(ctx, `SELECT `+policyColumns+` FROM entry_detection_policy WHERE id`))
	if err != nil {
		return DetectionPolicy{}, fmt.Errorf("entry: detection policy: %w", err)
	}
	return p, nil
}

// setDurations turns the row's seconds into the durations the code reads.
func (p *DetectionPolicy) setDurations(waveSecs, verdictSecs, cooldownSecs int) {
	p.WaveWindow = time.Duration(waveSecs) * time.Second
	p.VerdictWindow = time.Duration(verdictSecs) * time.Second
	p.RepairCooldown = time.Duration(cooldownSecs) * time.Second
}

// DetectionPolicyPatch is what the console sends. Every field is optional: the
// screen saves the knob that moved, and a knob it cannot show must not reset
// one it can.
type DetectionPolicyPatch struct {
	FailStrikes           *int  `json:"failStrikes"`
	WaveLimit             *int  `json:"waveLimit"`
	WaveWindowSeconds     *int  `json:"waveWindowSeconds"`
	VerdictWindowSeconds  *int  `json:"verdictWindowSeconds"`
	RepairCooldownSeconds *int  `json:"repairCooldownSeconds"`
	RequireTwoVantages    *bool `json:"requireTwoVantages"`
	IgnoreNetworkDown     *bool `json:"ignoreNetworkDown"`
	// SecondChance is keyed by kind (domain, box, cover); a kind left out,
	// and a knob left out inside one, keep what is stored.
	SecondChance map[string]SecondChancePatch `json:"secondChance"`
}

// VantageCounter says how many observation points have reported lately. The
// policy needs it for one decision only — whether two-vantage confirmation can
// be asked for at all — so it is declared here, on the consumer's side, and
// implemented over the probe dataset.
type VantageCounter interface {
	// Vantages returns the distinct observation points seen in [since, until].
	Vantages(ctx context.Context, since, until time.Time) ([]string, error)
}

// SetVantages wires the probe dataset's vantage list. Unset, the hub cannot
// tell how many observation points it has, and refuses to enable the
// two-vantage rule on that ground rather than guessing.
func (s *Service) SetVantages(v VantageCounter) { s.vantages = v }

// SaveDetectionPolicy applies a patch and returns the policy as it now stands.
//
// The two non-numeric fields are the interesting ones:
//
// `requireTwoVantages` is refused while the grid has fewer than two
// observation points. A hub that stored it anyway would have a screen saying
// "a ban needs two vantages" and a detector that could never see two — which
// means every eviction silently stops, and the screen that caused it reads as
// if nothing happened. Refusing says the one true thing: the second vantage
// does not exist yet.
//
// `ignoreNetworkDown` is refused outright when cleared. Our own network
// trouble is not evidence about a machine, and the off position of that switch
// retires healthy machines during an outage of ours. It is stored so the
// screen can show it, and locked so the screen cannot lie about it.
func (s *Service) SaveDetectionPolicy(ctx context.Context, patch DetectionPolicyPatch, actor string) (DetectionPolicy, error) {
	if patch.IgnoreNetworkDown != nil && !*patch.IgnoreNetworkDown {
		return DetectionPolicy{}, fmt.Errorf(
			"%w: пробник без сети не судит машину — выключатель заперт, иначе наша же авария сдаёт живые машины", ErrRejected)
	}
	if patch.RequireTwoVantages != nil && *patch.RequireTwoVantages {
		if s.vantages == nil {
			return DetectionPolicy{}, fmt.Errorf(
				"%w: хаб не знает, сколько у него точек наблюдения — включать подтверждение из двух нельзя", ErrRejected)
		}
		seen, err := s.Vantages(ctx)
		if err != nil {
			return DetectionPolicy{}, fmt.Errorf("entry: vantages: %w", err)
		}
		if len(seen) < 2 {
			return DetectionPolicy{}, fmt.Errorf(
				"%w: в сетке %d точка наблюдения — подтверждение из двух остановило бы любое выселение, а не ужесточило бы его",
				ErrRejected, len(seen))
		}
	}

	var chance [3]SecondChancePatch
	for kind, c := range patch.SecondChance {
		i := slices.Index(SuspectKinds, kind)
		if i < 0 {
			return DetectionPolicy{}, fmt.Errorf("%w: второго шанса для %q нет — бывает у домена, машины и прикрытия", ErrRejected, kind)
		}
		chance[i] = c
	}

	// COALESCE per column: a patch that omits a knob leaves the stored value,
	// without the handler having to read the row first and write it back —
	// which would lose a concurrent save of another knob.
	p, err := scanPolicy(s.pool.QueryRow(ctx, `
		UPDATE entry_detection_policy SET
			fail_strikes            = COALESCE($1, fail_strikes),
			wave_limit              = COALESCE($2, wave_limit),
			wave_window_seconds     = COALESCE($3, wave_window_seconds),
			verdict_window_seconds  = COALESCE($4, verdict_window_seconds),
			repair_cooldown_seconds = COALESCE($5, repair_cooldown_seconds),
			require_two_vantages    = COALESCE($6, require_two_vantages),
			domain_probe_every_seconds = COALESCE($9, domain_probe_every_seconds),
			domain_return_clean        = COALESCE($10, domain_return_clean),
			domain_dead_fails          = COALESCE($11, domain_dead_fails),
			domain_dead_after_seconds  = COALESCE($12, domain_dead_after_seconds),
			box_probe_every_seconds    = COALESCE($13, box_probe_every_seconds),
			box_return_clean           = COALESCE($14, box_return_clean),
			box_dead_fails             = COALESCE($15, box_dead_fails),
			box_dead_after_seconds     = COALESCE($16, box_dead_after_seconds),
			cover_probe_every_seconds  = COALESCE($17, cover_probe_every_seconds),
			cover_return_clean         = COALESCE($18, cover_return_clean),
			cover_dead_fails           = COALESCE($19, cover_dead_fails),
			cover_dead_after_seconds   = COALESCE($20, cover_dead_after_seconds),
			updated_at              = $7,
			updated_by              = $8
		WHERE id
		RETURNING `+policyColumns,
		patch.FailStrikes, patch.WaveLimit, patch.WaveWindowSeconds, patch.VerdictWindowSeconds,
		patch.RepairCooldownSeconds, patch.RequireTwoVantages, s.now(), clip(actor, 128),
		chance[0].ProbeEverySeconds, chance[0].ReturnClean, chance[0].DeadFails, chance[0].DeadAfterSeconds,
		chance[1].ProbeEverySeconds, chance[1].ReturnClean, chance[1].DeadFails, chance[1].DeadAfterSeconds,
		chance[2].ProbeEverySeconds, chance[2].ReturnClean, chance[2].DeadFails, chance[2].DeadAfterSeconds,
	))
	if err != nil {
		// A check constraint rejecting a value is the operator's answer, not
		// an internal fault: the bounds are in the migration, and naming them
		// twice would let the two drift.
		return DetectionPolicy{}, fmt.Errorf("%w: порог вне допустимых границ (%v)", ErrRejected, err)
	}

	s.policy.mu.Lock()
	s.policy.val, s.policy.at, s.policy.have = p, s.now(), true
	s.policy.mu.Unlock()
	return p, nil
}

// vantageWindow is how recently an observation point must have reported to
// count as part of the grid. A day: a vantage that has said nothing for longer
// is not one the hub can wait on for confirmation.
const vantageWindow = 24 * time.Hour

// Vantages lists the observation points that have reported lately, for the
// console's glass box: the active slots of entry_vantages seen in the probe
// dataset within the day. A region nobody registered, or a slot since
// retired, is not one — the registry is an allowlist. Empty means nobody
// active has probed in the window.
func (s *Service) Vantages(ctx context.Context) ([]string, error) {
	if s.vantages == nil {
		return nil, nil
	}
	now := s.now()
	seen, err := s.vantages.Vantages(ctx, now.Add(-vantageWindow), now.Add(clockSkew))
	if err != nil {
		return nil, err
	}
	active, err := s.activeVantageSet(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(seen))
	for _, r := range seen {
		if active[r] {
			out = append(out, r)
		}
	}
	return out, nil
}
