package entry

// reach.go takes in how clients' attempts to reach the service ended (roadmap
// 42 П9.9): the first metric of the entry-points console, and the only one
// measured from where it matters — the hands of people behind the censor,
// rather than one probing vantage of ours.
//
// # Why the client reports this at all, and why late
//
// An attempt that never got through cannot be reported while it is happening:
// there is no session to report over. So the client keeps the outcome of every
// attempt and flushes the lot with its next bundle fetch, which happens on any
// authenticated (re)connect. The share who never got through is therefore
// always known one session late — and known at all, which no server-side
// measurement can manage.
//
// # What this does NOT do
//
// Nothing. A reach row moves no relay, earns no replacement and feeds no
// decision in this phase, exactly as a failure report does not (§9): a client
// that lies skews a number in the console and changes nothing in the pool.
// The rows are the dataset the diagnosis of П9.4 and the playbooks of П9.5
// will be judged against.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
)

// MaxReachReports bounds one request's reach rows, as MaxFailReports bounds
// its failure reports.
const MaxReachReports = 16

// maxRelaysTried caps the dialled-relay count a client may claim. The batch is
// a couple of relays and the race retries, so a real attempt is far under
// this; the cap keeps one wrong client from dragging an average.
const maxRelaysTried = 64

// maxReachMillis caps the durations a client may claim, at ten minutes: longer
// than any attempt a person waits out, short enough that a bogus value cannot
// move a percentile far.
const maxReachMillis = 10 * 60 * 1000

// How an attempt ended (ClickHouse entry_reach.outcome).
const (
	// ReachDirect: the direct path to the gateway worked, no relay needed.
	ReachDirect = "direct"
	// ReachRelay: a relay carried the session.
	ReachRelay = "relay"
	// ReachFailed: nothing got through, reported from a later session.
	ReachFailed = "failed"
)

var knownOutcomes = map[string]bool{
	ReachDirect: true, ReachRelay: true, ReachFailed: true,
}

// reachKeyLabel names the HMAC key account ids are pseudonymised under before
// they reach the dataset (cryptobox.Derive), the same move the batch choice
// makes: the console needs to count people ("how many got through"), which
// takes a stable per-account value, and must not hold a list of who they are.
// Server-held, so the mapping cannot be recomputed from the rows alone.
const reachKeyLabel = "entry-reach"

// ReachReport is one attempt to reach the service, as a client reported it.
type ReachReport struct {
	Outcome     string
	Relay       string
	RelaysTried int
	ConnectMS   int
	RTTMS       int
	ObservedAt  time.Time
}

// ReachRecord is one row of the dataset, as the sink takes it.
type ReachRecord struct {
	TS time.Time
	// Account is the pseudonym, never the account id.
	Account     string
	Outcome     string
	Relay       string
	RelaysTried int
	ConnectMS   int
	RTTMS       int
}

// ReachSink takes reach rows into the dataset (ClickHouse entry_reach).
// Declared here on the consumer side; nil leaves the hub without the metric
// and costs nothing else.
type ReachSink interface {
	InsertEntryReach(ctx context.Context, rows []ReachRecord) error
}

// SetReachSink wires the dataset. Called once at startup; unset, reach reports
// are validated and dropped — the console then shows no doorway metric, and
// nothing else changes.
func (s *Service) SetReachSink(sink ReachSink) { s.reach = sink }

// RecordReach keeps what a client reported about its recent attempts. Returns
// how many rows were kept.
//
// Validation drops a row rather than refusing the batch: a client cannot act
// on "your telemetry was malformed", and the batch it is riding on must still
// be answered. Everything that reaches the sink is already clamped, so a query
// over the dataset never has to defend itself.
func (s *Service) RecordReach(ctx context.Context, account uuid.UUID, reports []ReachReport) (int, error) {
	if s.reach == nil {
		return 0, nil
	}
	if len(reports) > MaxReachReports {
		reports = reports[:MaxReachReports]
	}
	now := s.now()
	rows := make([]ReachRecord, 0, len(reports))
	for _, r := range reports {
		if !knownOutcomes[r.Outcome] {
			continue
		}
		// A relay name belongs to a relay outcome and nowhere else: a name on a
		// direct or failed attempt would attribute it to a relay that did not
		// carry it.
		relay := ""
		if r.Outcome == ReachRelay {
			if !nameRe.MatchString(r.Relay) {
				continue
			}
			relay = r.Relay
		}
		observed := r.ObservedAt
		if observed.After(now) || observed.Before(now.Add(-observedWindow)) {
			observed = now
		}
		rows = append(rows, ReachRecord{
			TS:          observed,
			Account:     s.reachPseudonym(account),
			Outcome:     r.Outcome,
			Relay:       relay,
			RelaysTried: clampInt(r.RelaysTried, 0, maxRelaysTried),
			ConnectMS:   clampInt(r.ConnectMS, 0, maxReachMillis),
			RTTMS:       clampInt(r.RTTMS, 0, maxReachMillis),
		})
	}
	if len(rows) == 0 {
		return 0, nil
	}
	if err := s.reach.InsertEntryReach(ctx, rows); err != nil {
		return 0, err
	}
	return len(rows), nil
}

// reachPseudonym is the account as the dataset knows it: HMAC(reachKey,
// account), hex. Stable, so distinct people can be counted; keyed, so the rows
// cannot be walked back to accounts without the server's key.
func (s *Service) reachPseudonym(account uuid.UUID) string {
	m := hmac.New(sha256.New, s.reachKey)
	m.Write(account[:])
	return hex.EncodeToString(m.Sum(nil)[:16])
}

// clampInt holds v inside [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
