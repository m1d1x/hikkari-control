package entry

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// A ban is the word of every point in Russia there is, not of whichever
// answered first (eun16/eun17: written off on 02.11, clean from Russia on
// 07.11). What a refusal sets off has no way back — a port burned, a fresh
// machine deleted at the hoster, a box handed back — so a refusal is asked
// again of the other live points in Russia, one at a time, up to
// maxConfirmPoints in all, and the majority of those that judged decides —
// with two instances behind a refusal, a prober among them (owner, 07.11):
//
//   - More points refuse than see the relay healthy, and either two points
//     refuse or the testers' reports see the relay blocked too: the refusal
//     stands, with the first refusal's words.
//   - One point refuses and the testers see nothing (a fresh relay nobody
//     rides yet): one instance, no refusal.
//   - As many or more see it healthy: the relay reads "unconfirmed" —
//     neither admitted nor refused. A caller that loops on blind relays asks
//     again; one that runs out of time ends on errNoVerdict, which no caller
//     acts on.
//
// Answers that say nothing about the relay (network_down, unknown) are not
// counted either way.

// verdictUnconfirmed is a refusal the points in Russia did not back by a
// majority.
const verdictUnconfirmed = "unconfirmed"

// maxConfirmPoints caps how many points one question is put to: a fleet of
// points is asked by a sample of ten, not by all of them in turn.
const maxConfirmPoints = 10

// confirmRoundWait bounds the wait for one more point: a live point answers
// its next poll within seconds; one that does not in this long is not there.
const confirmRoundWait = 2 * time.Minute

// liveCensorSlots is the active points in Russia whose admission worker polled
// within window: the ones a confirmation can expect an answer from.
func (s *Service) liveCensorSlots(ctx context.Context, window time.Duration) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT slot FROM entry_vantages
		WHERE status = 'active' AND in_russia AND last_admission_poll_at >= now() - make_interval(secs => $1)
		ORDER BY slot`, window.Seconds())
	if err != nil {
		return nil, fmt.Errorf("entry: live points: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var slot string
		if err := rows.Scan(&slot); err != nil {
			return nil, err
		}
		out = append(out, slot)
	}
	return out, rows.Err()
}

// confirmWindow is how recently a point must have polled to be counted live.
const confirmWindow = 10 * time.Minute

// askConfirmed queues one admission probe of names (job uuid.Nil for none),
// waits for it until deadline, and puts every refusal in it to the other live
// points in Russia. log, when set, narrates the wait.
func (s *Service) askConfirmed(ctx context.Context, job uuid.UUID, names []string, deadline time.Time, poll time.Duration, log func(string, ...any)) (map[string]string, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	task, err := s.createProbeTask(ctx, job, names, nil)
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

	live, err := s.liveCensorSlots(ctx, confirmWindow)
	if err != nil {
		return nil, err
	}
	answers := map[string]map[string]string{by: v}
	asked := []string{by}
	for len(asked) < maxConfirmPoints && hasUnasked(live, asked) {
		first := make([]string, len(refused))
		for i, n := range refused {
			first[i] = n + "=" + v[n]
		}
		log("%s: %s — спрашиваю ещё одну точку обзора в РФ (ответили: %s)", by, strings.Join(first, ", "), strings.Join(asked, ", "))
		confirm, err := s.createProbeTask(ctx, job, refused, asked)
		if err != nil {
			return nil, err
		}
		round := time.Now().Add(confirmRoundWait)
		if round.After(deadline) {
			round = deadline
		}
		vn, byn, err := s.waitProbe(ctx, confirm, round, poll)
		if err != nil {
			log("больше никто не ответил — решают %d точки", len(asked))
			break
		}
		answers[byn] = vn
		asked = append(asked, byn)
	}

	testers, _, err := s.testerVerdicts(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range refused {
		var ban, ok int
		var said []string
		for _, slot := range asked {
			w := answers[slot][n]
			said = append(said, slot+"="+orNoVerdict(w))
			switch {
			case admissionRefusal(w):
				ban++
			case admissionHealthy(w):
				ok++
			}
		}
		saw := admissionRefusal(testers[n])
		if ban > ok && instances(ban, saw) {
			continue // the majority refuses, two instances: the first refusal's words stand
		}
		if ban > ok {
			log("%s: %s — отказ один, тестеры блок не видят: двух инстанций нет", n, strings.Join(said, ", "))
		} else {
			log("%s: %s — большинства за отказ нет", n, strings.Join(said, ", "))
		}
		v[n] = verdictUnconfirmed
	}
	return v, nil
}

// hasUnasked reports whether a live point has not answered yet.
func hasUnasked(live, asked []string) bool {
	for _, l := range live {
		found := false
		for _, a := range asked {
			found = found || a == l
		}
		if !found {
			return true
		}
	}
	return false
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
