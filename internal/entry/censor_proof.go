package entry

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// A machine written off by hand for the censor — banned address, spotted
// fingerprint, cut domain — needs the vantage's word first, not only the
// testers'. On 02.11 eun16 (Hysteria2) and eun17 (AmneziaWG) were retired as
// ip_banned on two testers' timeouts while nobody asked the prober; on 07.11
// the same addresses came back clean from Russia and went into the pool again.
// A tester's network, client build or profile can fail on its own; a banned
// address fails for the vantage as well.
//
// So the console's and the shell's retire with a censor reason queue a fresh
// admission probe of the box's live relays — a refusal in it put to every
// live point in Russia, up to ten, the majority deciding (vantage_confirm.go)
// — and refuse when:
//   - the vantage sees any of them clean: it is not a ban, whatever the
//     testers saw;
//   - the vantage gives no verdict, or none answers: no evidence is not a
//     verdict (owner's decision 27.10, as errNoVerdict).
//
// A machine retired for another reason (rotated, other, provider_down) is not
// probed: the owner may let go of a working machine; nobody may call it banned
// without the prober saying so. The automatic passes are not gated
// here: they retire for a censor reason only on the vantage's own verdict.

// censorProofTimeout and censorProofPoll bound the wait for the vantage. A
// probe answered in 2–12 s on 07.11; three minutes covers a vantage busy with
// a round.
var (
	censorProofTimeout = 3 * time.Minute
	censorProofPoll    = 2 * time.Second
)

// ProveCensorDeath asks the vantage about box's live relays before it is
// retired for reason. It returns the verdicts that justify the write-off, nil
// when reason is not the censor's or the box has no live relay to ask about,
// and an ErrRejected naming what the vantage saw otherwise.
func (s *Service) ProveCensorDeath(ctx context.Context, box, reason string) (map[string]string, error) {
	if !slices.Contains(CensorDeaths, reason) {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT name FROM entry_relays
		WHERE box = $1 AND status IN ('alive', 'testing', 'draining', 'suspect')
		ORDER BY name`, box)
	if err != nil {
		return nil, fmt.Errorf("entry: relays of %s: %w", box, err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, nil // nobody rides it and nothing is left to ask about
	}

	// No job: a task tied to one is claimed only while the job runs, and
	// this wait is not a job. A refusal is put to the other live points.
	verdicts, err := s.askConfirmed(ctx, uuid.Nil, names, time.Now().Add(censorProofTimeout), censorProofPoll, nil)
	if errors.Is(err, errNoVerdict) {
		return nil, fmt.Errorf("%w: пробник из РФ не ответил за %s, а без его вердикта %s за %s не сдают; сдать без бана: причина rotated или other",
			ErrRejected, censorProofTimeout, box, reason)
	}
	if err != nil {
		return nil, err
	}

	var healthy, refused, blind []string
	for _, n := range names {
		v := verdicts[n]
		switch {
		case admissionHealthy(v):
			healthy = append(healthy, n+"="+v)
		case admissionRefusal(v):
			refused = append(refused, n+"="+v)
		default:
			if v == "" {
				v = "без вердикта"
			}
			blind = append(blind, n+"="+v)
		}
	}
	sort.Strings(healthy)
	switch {
	case len(healthy) > 0:
		return nil, fmt.Errorf("%w: пробник из РФ видит %s, а это не %s; сдать без бана: причина rotated или other",
			ErrRejected, strings.Join(healthy, ", "), reason)
	case len(refused) == 0:
		return nil, fmt.Errorf("%w: пробник из РФ не дал улик (%s), а без улик %s за %s не сдают",
			ErrRejected, strings.Join(blind, ", "), box, reason)
	}
	return verdicts, nil
}
