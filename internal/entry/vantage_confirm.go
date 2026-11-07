package entry

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// A ban is two points' word, never one's (eun16/eun17: written off on 02.11,
// clean from Russia on 07.11). A single point can be wrong about a relay for
// reasons of its own — its provider's path, a bad minute, a list that reached
// its network and not the subscribers' — and what a refusal sets off has no
// way back: a port burned, a fresh machine deleted at the hoster, a box
// handed back. So every refusal an admission probe gets (banned, spotted,
// cut) is asked again of another point in Russia, and stands only when that
// point refuses too.
//
//   - Both refuse: the refusal stands, with the first point's words.
//   - The second sees it healthy, or says nothing: there is no evidence, and
//     the relay reads "unconfirmed" — neither admitted nor refused. A caller
//     that loops on blind relays asks again; one that runs out of time ends on
//     errNoVerdict, which no caller acts on.
//   - Nobody else claims the confirmation by the deadline (one point left in
//     Russia): the same, no evidence. The task is taken back off the queue.

// verdictUnconfirmed is a refusal one point gave and a second did not back.
const verdictUnconfirmed = "unconfirmed"

// askConfirmed queues one admission probe of names (job uuid.Nil for none),
// waits for it until deadline, and has every refusal in it confirmed by a
// second point. log, when set, narrates the wait.
func (s *Service) askConfirmed(ctx context.Context, job uuid.UUID, names []string, deadline time.Time, poll time.Duration, log func(string, ...any)) (map[string]string, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	task, err := s.createProbeTask(ctx, job, names, "")
	if err != nil {
		return nil, err
	}
	log("проба допуска поставлена в очередь точке обзора")
	v, by, err := s.waitProbe(ctx, task, deadline, poll)
	if err != nil {
		return nil, err
	}
	var refused []string
	for _, n := range names {
		if admissionRefusal(v[n]) {
			refused = append(refused, n)
		}
	}
	if len(refused) == 0 {
		return v, nil
	}
	first := make([]string, len(refused))
	for i, n := range refused {
		first[i] = n + "=" + v[n]
	}
	log("%s: %s — переспрашиваю другую точку обзора в РФ", by, strings.Join(first, ", "))
	confirm, err := s.createProbeTask(ctx, job, refused, by)
	if err != nil {
		return nil, err
	}
	v2, by2, err := s.waitProbe(ctx, confirm, deadline, poll)
	if err != nil {
		for _, n := range refused {
			v[n] = verdictUnconfirmed
		}
		log("вторая точка обзора не ответила — отказ %s не подтверждён", by)
		return v, nil
	}
	for _, n := range refused {
		if admissionRefusal(v2[n]) {
			continue // two points refuse: the first one's words stand
		}
		log("%s: %s видит %s, а %s — %s: отказ не подтверждён", n, by2, orNoVerdict(v2[n]), by, v[n])
		v[n] = verdictUnconfirmed
	}
	return v, nil
}

// waitProbe waits for a task's verdicts until deadline. A task nobody claimed
// by then is taken back off the queue, and the wait ends on errNoVerdict.
func (s *Service) waitProbe(ctx context.Context, task uuid.UUID, deadline time.Time, poll time.Duration) (map[string]string, string, error) {
	for {
		v, by, err := s.probeVerdicts(ctx, task)
		if err != nil {
			return nil, "", err
		}
		if v != nil {
			return v, by, nil
		}
		if time.Now().After(deadline) {
			_, _ = s.pool.Exec(context.WithoutCancel(ctx),
				`DELETE FROM entry_probe_tasks WHERE id = $1 AND status = 'queued'`, task)
			return nil, "", fmt.Errorf("%w: ни одна точка обзора не ответила на пробу вовремя. пробник запущен?", errNoVerdict)
		}
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(poll):
		}
	}
}

func orNoVerdict(v string) string {
	if v == "" {
		return "без вердикта"
	}
	return v
}
